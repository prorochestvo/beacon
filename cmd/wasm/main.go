//go:build js && wasm

// Command wasm is the single-page WASM frontend for the FX Rate Monitor.
// It runs inside the browser via the Go WASM runtime, drives the DOM through
// window.fetch and innerHTML, and communicates with the server via /api/... routes.
package main

import (
	"context"
	"strconv"
	"sync"
	"syscall/js"
	"time"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/cmd/wasm/dom"
	"github.com/seilbekskindirov/monitor/cmd/wasm/ui"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// screen holds the teardown closures for a single active screen. When the user
// navigates away, Unmount calls every release closure to detach event listeners
// and free the underlying js.Func entries in the WASM function table.
type screen struct {
	releases []func()
}

func (s *screen) addRelease(fn func()) {
	s.releases = append(s.releases, fn)
}

func (s *screen) unmount() {
	for _, fn := range s.releases {
		fn()
	}
	s.releases = nil
}

// currentScreen is the single active screen. Navigation replaces it after
// calling Unmount on the outgoing one.
var currentScreen *screen

// mountScreen unmounts the previous screen (if any) and returns a fresh screen
// that the caller populates with release closures during handler binding.
func mountScreen() *screen {
	if currentScreen != nil {
		currentScreen.unmount()
	}
	currentScreen = &screen{}
	return currentScreen
}

func main() {
	client := apiclient.New(apiclient.NewDOMFetcher())
	wasmObj := js.Global().Get("Object").New()

	wasmObj.Set("renderSources", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		go runRenderSources(client)
		return nil
	}))

	wasmObj.Set("renderSourceDetail", js.FuncOf(func(_ js.Value, args []js.Value) any {
		name := ""
		if len(args) > 0 {
			name = args[0].String()
		}
		go runRenderSourceDetail(client, name)
		return nil
	}))

	wasmObj.Set("renderErrors", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		go runRenderErrors(client)
		return nil
	}))

	wasmObj.Set("renderMeSubscriptions", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		go runRenderMeSubscriptions(client)
		return nil
	}))

	wasmObj.Set("renderPublicSubscriptions", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		go runRenderPublicSubscriptions(client)
		return nil
	}))

	wasmObj.Set("renderMeSubscriptionsEdit", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		go runRenderMeSubscriptionsEdit(client)
		return nil
	}))

	js.Global().Set("_wasm", wasmObj)

	select {} // keep the WASM runtime alive
}

// runRenderSources fetches sources and stats in parallel, builds the page
// controller, renders the initial HTML, and binds event handlers.
// Must be called from a goroutine — never from the main goroutine.
func runRenderSources(client *apiclient.Client) {
	ctx := context.Background()
	doc := js.Global().Get("document")
	app := doc.Call("getElementById", "app")

	app.Set("innerHTML", "<p>Loading…</p>")

	var (
		srcs  []dto.SourceResponse
		stats dto.StatsResponse
		err1  error
		err2  error
		wg    sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		srcs, err1 = client.ListSources(ctx, 100)
	}()
	go func() {
		defer wg.Done()
		stats, err2 = client.Stats(ctx)
	}()
	wg.Wait()

	if err1 != nil {
		app.Set("innerHTML", "<p>Error loading sources: "+dom.Escape(err1.Error())+"</p>")
		return
	}
	if err2 != nil {
		app.Set("innerHTML", "<p>Error loading stats: "+dom.Escape(err2.Error())+"</p>")
		return
	}

	scr := mountScreen()
	page := application.NewSourcesPage(srcs, stats, client)
	app.Set("innerHTML", ui.RenderSources(page.State()))
	bindSourcesHandlers(doc, page, scr)
}

// bindSourcesHandlers wires all event handlers for the Sources List screen via
// dom.On.
//
// redraw is the only function that touches the DOM after initial render. It
// replaces only the inner table HTML, leaving filters and the stats line in
// place. The delegated click listener on #sources-table handles both the sort
// header (#sort-lastrun) and the active-toggle buttons inside the table. It is
// bound to the stable container div so it survives the repeated innerHTML
// replacements that destroy and recreate the inner nodes on every redraw.
func bindSourcesHandlers(doc js.Value, page *application.SourcesPage, scr *screen) {
	redraw := func() {
		tableDiv := doc.Call("getElementById", "sources-table")
		if tableDiv.IsNull() || tableDiv.IsUndefined() {
			return
		}
		tableDiv.Set("innerHTML", ui.RenderSourcesTable(page.State()))
	}

	bindID := func(id, event string, handler func(js.Value)) {
		el := doc.Call("getElementById", id)
		if el.IsNull() || el.IsUndefined() {
			return
		}
		scr.addRelease(dom.On(el, event, handler))
	}

	bindID("f-title", "input", func(ev js.Value) {
		page.OnFilterTitle(ev.Get("target").Get("value").String())
		redraw()
	})
	bindID("f-pair", "input", func(ev js.Value) {
		page.OnFilterPair(ev.Get("target").Get("value").String())
		redraw()
	})
	bindID("f-status", "change", func(ev js.Value) {
		page.OnFilterStatus(ev.Get("target").Get("value").String())
		redraw()
	})
	bindID("f-active", "change", func(ev js.Value) {
		page.OnFilterActive(ev.Get("target").Get("value").String())
		redraw()
	})

	tableDiv := doc.Call("getElementById", "sources-table")
	if !tableDiv.IsNull() && !tableDiv.IsUndefined() {
		scr.addRelease(dom.On(tableDiv, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			if target.Get("id").String() == "sort-lastrun" {
				page.ToggleSort()
				redraw()
				return
			}
			dataset := target.Get("dataset")
			if dataset.IsNull() || dataset.IsUndefined() {
				return
			}
			dataName := dataset.Get("name")
			dataActive := dataset.Get("active")
			if dataName.IsUndefined() || dataActive.IsUndefined() {
				return
			}
			name := dataName.String()
			active := dataActive.String() == "true"
			go func() {
				if _, err := page.ToggleActive(context.Background(), name, active); err != nil {
					js.Global().Get("console").Call("error", "toggleActive:", err.Error())
					return
				}
				redraw()
			}()
		}))
	}
}

// runRenderSourceDetail fetches the sources list (for the title lookup) and
// the first page of rates in parallel, then renders the skeleton immediately.
// Subscriptions and daily events are loaded in separate goroutines AFTER the
// skeleton is in the DOM so those goroutines can safely target the stable
// #subs-section and #daily-events-section divs.
func runRenderSourceDetail(client *apiclient.Client, name string) {
	ctx := context.Background()
	doc := js.Global().Get("document")
	app := doc.Call("getElementById", "app")

	app.Set("innerHTML", "<p>Loading…</p>")

	var (
		srcs  []dto.SourceResponse
		rates []dto.RateResponse
		err1  error
		err2  error
		wg    sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		srcs, err1 = client.ListSources(ctx, 100)
	}()
	go func() {
		defer wg.Done()
		rates, err2 = client.ListRates(ctx, name, 50)
	}()
	wg.Wait()

	if err1 != nil {
		app.Set("innerHTML", "<p>Error loading sources: "+dom.Escape(err1.Error())+"</p>")
		return
	}
	if err2 != nil {
		app.Set("innerHTML", "<p>Error loading rates: "+dom.Escape(err2.Error())+"</p>")
		return
	}

	scr := mountScreen()
	page := application.NewSourceDetailPage(name, srcs, rates, client)

	// Render the skeleton synchronously before starting async goroutines.
	// The subs and daily-events goroutines below depend on #subs-section and
	// #daily-events-section being present in the DOM.
	app.Set("innerHTML", ui.RenderSourceDetail(page.State()))

	// Load subscriptions and daily events in parallel after the skeleton is
	// rendered. Each goroutine updates its own stable section div.
	go func() {
		if err := page.LoadSubsPage(ctx, 1); err != nil {
			subsSection := doc.Call("getElementById", "subs-section")
			if !subsSection.IsNull() && !subsSection.IsUndefined() {
				subsSection.Set("innerHTML", "<p>Error loading subscriptions: "+dom.Escape(err.Error())+"</p>")
			}
			return
		}
		subsSection := doc.Call("getElementById", "subs-section")
		if !subsSection.IsNull() && !subsSection.IsUndefined() {
			subsSection.Set("innerHTML", ui.RenderSubsSection(page.State()))
		}
	}()

	go func() {
		if err := page.LoadDailyEventsPage(ctx, 1); err != nil {
			evSection := doc.Call("getElementById", "daily-events-section")
			if !evSection.IsNull() && !evSection.IsUndefined() {
				evSection.Set("innerHTML", "<p>Error loading daily events: "+dom.Escape(err.Error())+"</p>")
			}
			return
		}
		evSection := doc.Call("getElementById", "daily-events-section")
		if !evSection.IsNull() && !evSection.IsUndefined() {
			evSection.Set("innerHTML", ui.RenderDailyEventsSection(page.State()))
		}
	}()

	bindSourceDetailHandlers(doc, page, scr)
}

// bindSourceDetailHandlers wires all event handlers for the Source Detail
// screen. A delegated click on #rates-table handles the sort header. A
// delegated click on each paginated section reads data-section and data-page
// to route the page change to the correct Load* call.
func bindSourceDetailHandlers(doc js.Value, page *application.SourceDetailPage, scr *screen) {
	redrawRates := func() {
		ratesTable := doc.Call("getElementById", "rates-table")
		if ratesTable.IsNull() || ratesTable.IsUndefined() {
			return
		}
		ratesTable.Set("innerHTML", ui.RenderRatesTable(page.State()))
	}

	rateFilter := doc.Call("getElementById", "rate-filter")
	if !rateFilter.IsNull() && !rateFilter.IsUndefined() {
		scr.addRelease(dom.On(rateFilter, "input", func(ev js.Value) {
			page.OnRateFilter(ev.Get("target").Get("value").String())
			redrawRates()
		}))
	}

	ratesTable := doc.Call("getElementById", "rates-table")
	if !ratesTable.IsNull() && !ratesTable.IsUndefined() {
		scr.addRelease(dom.On(ratesTable, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			if target.Get("id").String() == "rate-sort-header" {
				page.ToggleRateSort()
				redrawRates()
			}
		}))
	}

	bindPaginatedSection(doc, "subs-section", page, scr)
	bindPaginatedSection(doc, "daily-events-section", page, scr)
}

// runRenderErrors renders the Errors skeleton immediately, then loads
// execution errors and event errors in two parallel goroutines that each update
// their own stable section div.
func runRenderErrors(client *apiclient.Client) {
	ctx := context.Background()
	doc := js.Global().Get("document")
	app := doc.Call("getElementById", "app")

	app.Set("innerHTML", "<p>Loading…</p>")

	scr := mountScreen()
	page := application.NewErrorsPage(client)
	app.Set("innerHTML", ui.RenderErrors(page.State()))

	go func() {
		if err := page.LoadExecPage(ctx, 1); err != nil {
			sec := doc.Call("getElementById", "exec-errors-section")
			if !sec.IsNull() && !sec.IsUndefined() {
				sec.Set("innerHTML", "<p>Error loading execution errors: "+dom.Escape(err.Error())+"</p>")
			}
			return
		}
		sec := doc.Call("getElementById", "exec-errors-section")
		if !sec.IsNull() && !sec.IsUndefined() {
			sec.Set("innerHTML", ui.RenderExecErrorsSection(page.State()))
		}
	}()

	go func() {
		if err := page.LoadEventPage(ctx, 1); err != nil {
			sec := doc.Call("getElementById", "event-errors-section")
			if !sec.IsNull() && !sec.IsUndefined() {
				sec.Set("innerHTML", "<p>Error loading event errors: "+dom.Escape(err.Error())+"</p>")
			}
			return
		}
		sec := doc.Call("getElementById", "event-errors-section")
		if !sec.IsNull() && !sec.IsUndefined() {
			sec.Set("innerHTML", ui.RenderEventErrorsSection(page.State()))
		}
	}()

	bindErrorsSections(doc, page, scr)
}

// bindErrorsSections attaches delegated click handlers to the two paginated
// error sections. Each button carries data-section and data-page; the handler
// dispatches to LoadExecPage or LoadEventPage based on the section value.
func bindErrorsSections(doc js.Value, page *application.ErrorsPage, scr *screen) {
	bindErrorSection := func(sectionID string) {
		section := doc.Call("getElementById", sectionID)
		if section.IsNull() || section.IsUndefined() {
			return
		}
		scr.addRelease(dom.On(section, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			dataset := target.Get("dataset")
			if dataset.IsNull() || dataset.IsUndefined() {
				return
			}
			sectionAttr := dataset.Get("section")
			pageAttr := dataset.Get("page")
			if sectionAttr.IsUndefined() || pageAttr.IsUndefined() {
				return
			}
			sectionName := sectionAttr.String()
			pageNum, err := strconv.Atoi(pageAttr.String())
			if err != nil || pageNum < 1 {
				return
			}
			ctx := context.Background()
			switch sectionName {
			case "exec":
				go func() {
					if err := page.LoadExecPage(ctx, pageNum); err != nil {
						sec := doc.Call("getElementById", "exec-errors-section")
						if !sec.IsNull() && !sec.IsUndefined() {
							sec.Set("innerHTML", "<p>Error loading execution errors: "+dom.Escape(err.Error())+"</p>")
						}
						return
					}
					sec := doc.Call("getElementById", "exec-errors-section")
					if !sec.IsNull() && !sec.IsUndefined() {
						sec.Set("innerHTML", ui.RenderExecErrorsSection(page.State()))
					}
				}()
			case "event":
				go func() {
					if err := page.LoadEventPage(ctx, pageNum); err != nil {
						sec := doc.Call("getElementById", "event-errors-section")
						if !sec.IsNull() && !sec.IsUndefined() {
							sec.Set("innerHTML", "<p>Error loading event errors: "+dom.Escape(err.Error())+"</p>")
						}
						return
					}
					sec := doc.Call("getElementById", "event-errors-section")
					if !sec.IsNull() && !sec.IsUndefined() {
						sec.Set("innerHTML", ui.RenderEventErrorsSection(page.State()))
					}
				}()
			}
		}))
	}

	bindErrorSection("exec-errors-section")
	bindErrorSection("event-errors-section")
}

// uploadUserProfile reads the browser's resolved IANA timezone and BCP-47
// locale from Intl.DateTimeFormat() and POSTs both to /api/me/profile.
// Fire-and-forget — the caller wraps this in `go` and discards the result.
// The notifier falls back to UTC when no profile is configured, so a failed
// upload is a soft regression, not a blocking error.
//
// The whole flow lives in the WASM client because the Telegram Bot API does
// not reliably expose either timezone or locale. By project policy this
// upload never sends username / display name — see the no-PII memory.
func uploadUserProfile(ctx context.Context, client *apiclient.Client, initData string) {
	if initData == "" {
		return // initData is required by the server-side HMAC verifier.
	}
	intl := js.Global().Get("Intl")
	if intl.IsNull() || intl.IsUndefined() {
		return
	}
	dtf := intl.Get("DateTimeFormat")
	if dtf.IsNull() || dtf.IsUndefined() {
		return
	}
	opts := dtf.New().Call("resolvedOptions")
	if opts.IsNull() || opts.IsUndefined() {
		return
	}

	var tz, locale string
	if v := opts.Get("timeZone"); !v.IsNull() && !v.IsUndefined() {
		tz = v.String()
	}
	if v := opts.Get("locale"); !v.IsNull() && !v.IsUndefined() {
		locale = v.String()
	}
	if tz == "" {
		// Timezone is the load-bearing field — without it the notifier has
		// nothing to localise. Skip the call rather than persisting an empty row.
		return
	}
	if err := client.UpdateMeProfile(ctx, initData, tz, locale); err != nil {
		js.Global().Get("console").Call("warn", "me-profile upload:", err.Error())
	}
}

// readInitData reads window.Telegram.WebApp.initData when the page is opened
// inside Telegram. Falls back to the ?initData= page-URL query parameter so a
// developer can drive the Mini App from a normal browser tab during local
// testing; the value is always forwarded to the API via the
// X-Telegram-Init-Data header (never as a URL parameter on the API call
// itself — the server's URL fallback was removed in the 2026-05-21 audit).
//
// WARNING: when the page-URL fallback fires, the signed initData payload
// lands in the browser address bar, history, and the static-asset server's
// access log for up to its 24h validity window. Use it only for local dev;
// never link production users to a URL carrying initData.
func readInitData() string {
	telegram := js.Global().Get("Telegram")
	if !telegram.IsNull() && !telegram.IsUndefined() {
		webApp := telegram.Get("WebApp")
		if !webApp.IsNull() && !webApp.IsUndefined() {
			if v := webApp.Get("initData"); !v.IsNull() && !v.IsUndefined() {
				if s := v.String(); s != "" {
					return s
				}
			}
		}
	}
	// Page-URL fallback for local dev — see godoc warning above.
	search := js.Global().Get("location").Get("search")
	if search.IsNull() || search.IsUndefined() {
		return ""
	}
	params := js.Global().Get("URLSearchParams").New(search)
	v := params.Call("get", "initData")
	if v.IsNull() || v.IsUndefined() {
		return ""
	}
	if console := js.Global().Get("console"); !console.IsNull() && !console.IsUndefined() {
		console.Call("warn",
			"[dev] initData sourced from page URL — do not use in production")
	}
	return v.String()
}

// callWebAppIfDefined calls ready() and expand() on window.Telegram.WebApp if
// it is defined. Outside Telegram (e.g. a regular browser tab during local dev)
// those objects may not exist; the guard prevents a JavaScript exception.
func callWebAppIfDefined() {
	telegram := js.Global().Get("Telegram")
	if telegram.IsNull() || telegram.IsUndefined() {
		return
	}
	webApp := telegram.Get("WebApp")
	if webApp.IsNull() || webApp.IsUndefined() {
		return
	}
	webApp.Call("ready")
	webApp.Call("expand")
}

// runRenderMeSubscriptions is the entry point for the Telegram Mini App screen.
// It reads initData once at mount, calls WebApp.ready/expand, renders the
// skeleton, loads the first page of subscriptions in this goroutine, then
// launches a background goroutine for the sparkline chart. Must be called from
// a goroutine — never from the main goroutine.
func runRenderMeSubscriptions(client *apiclient.Client) {
	callWebAppIfDefined()

	initData := readInitData()

	// screenCtx is cancelled when the screen is unmounted, which cancels any
	// in-flight sparkline chart fetch so the goroutine exits cleanly instead
	// of writing stale data back to a screen the user has already left.
	screenCtx, cancelScreen := context.WithCancel(context.Background())
	doc := js.Global().Get("document")
	app := doc.Call("getElementById", "app")

	// The inline .app-loader markup from subscriptions.html remains visible
	// until this line replaces #app innerHTML. No intermediate "Loading…"
	// text is written first — that would briefly destroy the loader between
	// the two writes and flash an ugly text frame on the user.
	scr := mountScreen()
	scr.addRelease(cancelScreen)
	// MeSubscriptionsBatchSize lets the modal join all of the user's pairs
	// against in-memory items; the list itself is no longer rendered.
	page := application.NewMeSubscriptionsPage(client, initData, application.MeSubscriptionsBatchSize)

	// alive tracks whether this screen is still the active one. It is set to
	// false in a release closure so that a stale chart-fetch goroutine that
	// completes after the user navigated away does not write into the new
	// screen's DOM. WASM is single-threaded, so a plain bool is sufficient.
	alive := true
	scr.addRelease(func() { alive = false })

	// lockBodyScroll locks or unlocks document.body overflow. On iOS Telegram
	// WebView the body continues scrolling under a position:fixed modal without
	// this guard.
	lockBodyScroll := func(lock bool) {
		body := doc.Call("querySelector", "body")
		if body.IsNull() || body.IsUndefined() {
			return
		}
		style := body.Get("style")
		if lock {
			style.Set("overflow", "hidden")
		} else {
			style.Set("overflow", "")
		}
	}
	// Ensure the body is unlocked when the screen is torn down, covering the
	// case where the user navigates away with a modal still open.
	scr.addRelease(func() { lockBodyScroll(false) })

	// Render the skeleton immediately so the user sees a responsive UI.
	app.Set("innerHTML", ui.RenderMeSubscriptions(page.State()))

	redrawChart := func() {
		if !alive {
			return
		}
		chartDiv := doc.Call("getElementById", "me-sparkline-chart")
		if chartDiv.IsNull() || chartDiv.IsUndefined() {
			// The screen has been replaced by another screen; drop the write.
			return
		}
		chartDiv.Set("innerHTML", ui.RenderSparklineSlot(page.State()))
	}

	// loadSparklineChart fetches /api/me/rates/chart in a single goroutine.
	// It derives its timeout from screenCtx so navigation cancels the fetch
	// instead of letting the goroutine run until the 15s deadline fires.
	loadSparklineChart := func() {
		go func() {
			fetchCtx, fetchCancel := context.WithTimeout(screenCtx, 15*time.Second)
			defer fetchCancel()
			if err := page.LoadSparklineChart(fetchCtx); err != nil {
				js.Global().Get("console").Call("warn", "sparkline chart fetch:", err.Error())
			}
			redrawChart()
		}()
	}

	// Fire-and-forget profile upload (timezone + BCP-47 locale) so the server
	// can localise notification timestamps and, later, message text. Errors are
	// console.warn only — the page must not block on this since notifications
	// still work in UTC when the upload fails.
	go uploadUserProfile(screenCtx, client, initData)

	// Load the first page of subscriptions. Items are used by the modal's
	// condition-badges join; they are not rendered as a list.
	if err := page.LoadInitial(screenCtx); err != nil {
		js.Global().Get("console").Call("warn", "me-subs LoadInitial:", err.Error())
	}
	loadSparklineChart()

	bindMeSubsHandlers(screenCtx, doc, page, scr, &alive, lockBodyScroll)
}

// bindMeSubsHandlers wires all event handlers for the Mini App subscriptions screen.
//
// Row click / keydown: a delegated handler on #me-sparkline-chart walks up to
// the nearest .sparkline-row via Element.closest, reads its data-pair attribute,
// and calls OpenPairModal followed by a modal slot redraw. Enter and Space on a
// focused row open the modal via the same path so keyboard users are covered.
//
// Modal close: a delegated click handler on #me-pair-modal-slot checks the target
// ID. Clicks on #me-pair-modal-close branch on HistoryOpen: when history is open
// the click calls CloseHistory (one press); otherwise it calls ClosePairModal.
// Clicks on #me-pair-modal-backdrop always call ClosePairModal unconditionally
// (fast escape hatch). Content-area clicks are ignored.
//
// History navigation: clicks on #me-pair-modal-history, #me-pair-history-prev,
// #me-pair-history-next, #me-pair-history-source-all, and elements matching
// .me-pair-history-source-chip are handled inside the same delegated listener
// on #me-pair-modal-slot.
//
// Escape: a document-level keydown listener closes the history view first (one press),
// then closes the modal (second press) when both are open.
//
// ctx is the screen-lifetime context (screenCtx from runRenderMeSubscriptions); it is
// used when spawning goroutines for history fetches so they are cancelled on unmount.
//
// alive is a pointer into runRenderMeSubscriptions's alive bool; when false the
// screen has been unmounted and DOM writes are skipped. lockBodyScroll is called
// with true/false to prevent iOS scroll bleed under the modal overlay.
func bindMeSubsHandlers(
	ctx context.Context,
	doc js.Value,
	page *application.MeSubscriptionsPage,
	scr *screen,
	alive *bool,
	lockBodyScroll func(bool),
) {
	redrawModal := func() {
		if !*alive {
			return
		}
		slot := doc.Call("getElementById", "me-pair-modal-slot")
		if slot.IsNull() || slot.IsUndefined() {
			return
		}
		slot.Set("innerHTML", ui.RenderPairModal(page.State()))
	}

	openModal := func(pair string) {
		page.OpenPairModal(pair)
		lockBodyScroll(true)
		redrawModal()
	}

	closeModal := func() {
		page.ClosePairModal()
		lockBodyScroll(false)
		redrawModal()
	}

	redrawChartSlot := func() {
		if !*alive {
			return
		}
		chartDiv := doc.Call("getElementById", "me-sparkline-chart")
		if chartDiv.IsNull() || chartDiv.IsUndefined() {
			return
		}
		chartDiv.Set("innerHTML", ui.RenderSparklineSlot(page.State()))
	}

	// Gear button: navigate to the subscription editor screen.
	manageBtn := doc.Call("getElementById", "me-manage")
	if !manageBtn.IsNull() && !manageBtn.IsUndefined() {
		scr.addRelease(dom.On(manageBtn, "click", func(_ js.Value) {
			js.Global().Get("_wasm").Call("renderMeSubscriptionsEdit")
		}))
	}

	// Delegated click handler on the chart container: intercepts period-chip
	// clicks first, then opens modal for the nearest .sparkline-row ancestor.
	chartDiv := doc.Call("getElementById", "me-sparkline-chart")
	if !chartDiv.IsNull() && !chartDiv.IsUndefined() {
		scr.addRelease(dom.On(chartDiv, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			// Period-chip click: intercept before the .sparkline-row closest walk.
			chipEl := target.Call("closest", ".period-chip")
			if !chipEl.IsNull() && !chipEl.IsUndefined() {
				periodAttr := chipEl.Get("dataset").Get("period")
				if !periodAttr.IsUndefined() {
					periodVal, err := strconv.Atoi(periodAttr.String())
					if err == nil {
						go func() {
							fetchCtx, fetchCancel := context.WithTimeout(ctx, 15*time.Second)
							defer fetchCancel()
							if err := page.SetPeriod(fetchCtx, periodVal); err != nil {
								js.Global().Get("console").Call("warn", "period chip:", err.Error())
							}
							redrawChartSlot()
						}()
					}
					return
				}
			}
			row := target.Call("closest", ".sparkline-row")
			if row.IsNull() || row.IsUndefined() {
				return
			}
			dataset := row.Get("dataset")
			if dataset.IsNull() || dataset.IsUndefined() {
				return
			}
			pairAttr := dataset.Get("pair")
			if pairAttr.IsUndefined() {
				return
			}
			openModal(pairAttr.String())
		}))

		// Keyboard handler: Enter or Space on a focused .sparkline-row opens the modal.
		scr.addRelease(dom.On(chartDiv, "keydown", func(ev js.Value) {
			key := ev.Get("key").String()
			if key != "Enter" && key != " " {
				return
			}
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			row := target.Call("closest", ".sparkline-row")
			if row.IsNull() || row.IsUndefined() {
				return
			}
			dataset := row.Get("dataset")
			if dataset.IsNull() || dataset.IsUndefined() {
				return
			}
			pairAttr := dataset.Get("pair")
			if pairAttr.IsUndefined() {
				return
			}
			openModal(pairAttr.String())
		}))
	}

	// Delegated click handler on the modal slot: close on backdrop or close-button;
	// navigate history via the history action buttons.
	modalSlot := doc.Call("getElementById", "me-pair-modal-slot")
	if !modalSlot.IsNull() && !modalSlot.IsUndefined() {
		scr.addRelease(dom.On(modalSlot, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			id := target.Get("id").String()
			switch id {
			case "me-pair-modal-close":
				if page.State().HistoryOpen {
					page.CloseHistory()
					redrawModal()
				} else {
					closeModal()
				}
			case "me-pair-modal-backdrop":
				closeModal()
			case "me-pair-modal-history":
				// Open the history view: fetch page 1 in a goroutine and redraw.
				go func() {
					fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					if err := page.OpenHistory(fetchCtx); err != nil {
						js.Global().Get("console").Call("warn", "history fetch:", err.Error())
					}
					redrawModal()
				}()
			case "me-pair-history-prev":
				go func() {
					fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					if err := page.HistoryPrevPage(fetchCtx); err != nil {
						js.Global().Get("console").Call("warn", "history prev:", err.Error())
					}
					redrawModal()
				}()
			case "me-pair-history-next":
				go func() {
					fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					if err := page.HistoryNextPage(fetchCtx); err != nil {
						js.Global().Get("console").Call("warn", "history next:", err.Error())
					}
					redrawModal()
				}()
				// Clicks inside .me-pair-modal-card are ignored — content-area
				// interaction must not close the overlay.
			default:
				// Handle source-filter chip clicks via closest() to cover child nodes.
				chip := target.Call("closest", ".me-pair-history-source-chip")
				if chip.IsNull() || chip.IsUndefined() {
					return
				}
				title := chip.Get("dataset").Get("source").String()
				go func() {
					fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					if err := page.SetHistorySourceTitle(fetchCtx, title); err != nil {
						js.Global().Get("console").Call("warn", "history source filter:", err.Error())
					}
					redrawModal()
				}()
			}
		}))
	}

	// Document-level Escape handler: one press closes the history view when open;
	// a second press (or a single press when history is not open) closes the modal.
	// Registered via dom.On and released via scr.addRelease so it is cleaned
	// up on screen unmount even when navigating away with a modal open.
	// NOTE: if a future feature adds another document-level keydown listener
	// (e.g. a global search shortcut), the dispatch order becomes load-bearing;
	// consider an event-bus pattern at that point.
	scr.addRelease(dom.On(doc, "keydown", func(ev js.Value) {
		if ev.Get("key").String() != "Escape" {
			return
		}
		st := page.State()
		if st.OpenPair == nil {
			return
		}
		if st.HistoryOpen {
			page.CloseHistory()
			redrawModal()
			return
		}
		closeModal()
	}))
}

// runRenderPublicSubscriptions is the entry point for the unauthenticated guest
// landing page. It renders the skeleton immediately, then loads the first page of
// the public sparkline chart in a background goroutine. No initData is read; no
// profile upload is performed. Must be called from a goroutine — never from the
// main goroutine.
func runRenderPublicSubscriptions(client *apiclient.Client) {
	screenCtx, cancelScreen := context.WithCancel(context.Background())
	doc := js.Global().Get("document")
	app := doc.Call("getElementById", "app")

	scr := mountScreen()
	scr.addRelease(cancelScreen)

	page := application.NewPublicSubscriptionsPage(client)

	// alive guards DOM writes from the chart-fetch goroutine against stale screens.
	alive := true
	scr.addRelease(func() { alive = false })

	lockBodyScroll := func(lock bool) {
		body := doc.Call("querySelector", "body")
		if body.IsNull() || body.IsUndefined() {
			return
		}
		style := body.Get("style")
		if lock {
			style.Set("overflow", "hidden")
		} else {
			style.Set("overflow", "")
		}
	}
	scr.addRelease(func() { lockBodyScroll(false) })

	app.Set("innerHTML", ui.RenderPublicSubscriptions(page.State()))

	redrawChart := func() {
		if !alive {
			return
		}
		chartDiv := doc.Call("getElementById", "public-sparkline-chart")
		if chartDiv.IsNull() || chartDiv.IsUndefined() {
			return
		}
		chartDiv.Set("innerHTML", ui.RenderPublicSparklineSlot(page.State()))
	}

	redrawPagination := func() {
		if !alive {
			return
		}
		paginationDiv := doc.Call("getElementById", "public-pagination")
		if paginationDiv.IsNull() || paginationDiv.IsUndefined() {
			return
		}
		paginationDiv.Set("innerHTML", ui.RenderPublicPagination(page.State()))
	}

	// Load the first page in a background goroutine so the skeleton is visible
	// immediately. screenCtx cancels the fetch when the user navigates away.
	go func() {
		fetchCtx, fetchCancel := context.WithTimeout(screenCtx, 15*time.Second)
		defer fetchCancel()
		if err := page.LoadPage(fetchCtx, 1); err != nil {
			js.Global().Get("console").Call("warn", "public chart fetch:", err.Error())
		}
		redrawChart()
		redrawPagination()
	}()

	bindPublicSubsHandlers(screenCtx, doc, page, scr, &alive, lockBodyScroll)
}

// bindPublicSubsHandlers wires all event handlers for the public subscriptions screen.
//
// Row click / keydown: a delegated handler on #public-sparkline-chart walks up to
// the nearest .sparkline-row via Element.closest, reads its data-pair attribute,
// and calls OpenPairModal followed by a modal slot redraw.
//
// Modal close: a delegated click handler on #public-pair-modal-slot closes the
// modal on clicks to #public-pair-modal-close or #public-pair-modal-backdrop.
//
// Pagination: a delegated click handler on #public-pagination reads data-section
// and data-page to load the target page.
//
// Escape: a document-level keydown listener closes the modal.
//
// ctx is the screen-lifetime context; alive is a pointer into
// runRenderPublicSubscriptions's alive bool; lockBodyScroll prevents iOS scroll bleed.
func bindPublicSubsHandlers(
	ctx context.Context,
	doc js.Value,
	page *application.PublicSubscriptionsPage,
	scr *screen,
	alive *bool,
	lockBodyScroll func(bool),
) {
	redrawModal := func() {
		if !*alive {
			return
		}
		slot := doc.Call("getElementById", "public-pair-modal-slot")
		if slot.IsNull() || slot.IsUndefined() {
			return
		}
		slot.Set("innerHTML", ui.RenderPublicPairModal(page.State()))
	}

	redrawChart := func() {
		if !*alive {
			return
		}
		chartDiv := doc.Call("getElementById", "public-sparkline-chart")
		if chartDiv.IsNull() || chartDiv.IsUndefined() {
			return
		}
		chartDiv.Set("innerHTML", ui.RenderPublicSparklineSlot(page.State()))
	}

	redrawPagination := func() {
		if !*alive {
			return
		}
		paginationDiv := doc.Call("getElementById", "public-pagination")
		if paginationDiv.IsNull() || paginationDiv.IsUndefined() {
			return
		}
		paginationDiv.Set("innerHTML", ui.RenderPublicPagination(page.State()))
	}

	openModal := func(pair string) {
		page.OpenPairModal(pair)
		lockBodyScroll(true)
		redrawModal()
	}

	closeModal := func() {
		page.ClosePairModal()
		lockBodyScroll(false)
		redrawModal()
	}

	// Delegated click on the chart container: intercepts period-chip clicks
	// first, then opens modal for the nearest .sparkline-row ancestor.
	chartDiv := doc.Call("getElementById", "public-sparkline-chart")
	if !chartDiv.IsNull() && !chartDiv.IsUndefined() {
		scr.addRelease(dom.On(chartDiv, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			// Period-chip click: intercept before the .sparkline-row closest walk.
			chipEl := target.Call("closest", ".period-chip")
			if !chipEl.IsNull() && !chipEl.IsUndefined() {
				periodAttr := chipEl.Get("dataset").Get("period")
				if !periodAttr.IsUndefined() {
					periodVal, err := strconv.Atoi(periodAttr.String())
					if err == nil {
						go func() {
							fetchCtx, fetchCancel := context.WithTimeout(ctx, 15*time.Second)
							defer fetchCancel()
							if err := page.SetPeriod(fetchCtx, periodVal); err != nil {
								js.Global().Get("console").Call("warn", "period chip:", err.Error())
							}
							redrawChart()
							redrawPagination()
						}()
					}
					return
				}
			}
			row := target.Call("closest", ".sparkline-row")
			if row.IsNull() || row.IsUndefined() {
				return
			}
			dataset := row.Get("dataset")
			if dataset.IsNull() || dataset.IsUndefined() {
				return
			}
			pairAttr := dataset.Get("pair")
			if pairAttr.IsUndefined() {
				return
			}
			openModal(pairAttr.String())
		}))

		// Keyboard handler: Enter or Space on a focused .sparkline-row opens the modal.
		scr.addRelease(dom.On(chartDiv, "keydown", func(ev js.Value) {
			key := ev.Get("key").String()
			if key != "Enter" && key != " " {
				return
			}
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			row := target.Call("closest", ".sparkline-row")
			if row.IsNull() || row.IsUndefined() {
				return
			}
			dataset := row.Get("dataset")
			if dataset.IsNull() || dataset.IsUndefined() {
				return
			}
			pairAttr := dataset.Get("pair")
			if pairAttr.IsUndefined() {
				return
			}
			openModal(pairAttr.String())
		}))
	}

	// Delegated click on the modal slot: close on backdrop or close-button.
	modalSlot := doc.Call("getElementById", "public-pair-modal-slot")
	if !modalSlot.IsNull() && !modalSlot.IsUndefined() {
		scr.addRelease(dom.On(modalSlot, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			id := target.Get("id").String()
			switch id {
			case "public-pair-modal-close", "public-pair-modal-backdrop":
				closeModal()
			}
		}))
	}

	// Delegated click on pagination: load the target page.
	paginationDiv := doc.Call("getElementById", "public-pagination")
	if !paginationDiv.IsNull() && !paginationDiv.IsUndefined() {
		scr.addRelease(dom.On(paginationDiv, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			dataset := target.Get("dataset")
			if dataset.IsNull() || dataset.IsUndefined() {
				return
			}
			pageAttr := dataset.Get("page")
			if pageAttr.IsUndefined() {
				return
			}
			pageNum, err := strconv.Atoi(pageAttr.String())
			if err != nil || pageNum < 1 {
				return
			}
			go func() {
				fetchCtx, fetchCancel := context.WithTimeout(ctx, 15*time.Second)
				defer fetchCancel()
				if err := page.LoadPage(fetchCtx, pageNum); err != nil {
					js.Global().Get("console").Call("warn", "public chart page load:", err.Error())
				}
				redrawChart()
				redrawPagination()
				// Auto-close any open modal after a page change — the row may no
				// longer appear in the new page.
				redrawModal()
			}()
		}))
	}

	// Document-level Escape handler: closes the modal when open.
	scr.addRelease(dom.On(doc, "keydown", func(ev js.Value) {
		if ev.Get("key").String() != "Escape" {
			return
		}
		if page.State().OpenPair == nil {
			return
		}
		closeModal()
	}))
}

// bindPaginatedSection attaches a delegated click handler to the named section
// div. Buttons inside the section carry data-section and data-page attributes;
// the handler reads both to dispatch the page change.
func bindPaginatedSection(doc js.Value, sectionID string, page *application.SourceDetailPage, scr *screen) {
	section := doc.Call("getElementById", sectionID)
	if section.IsNull() || section.IsUndefined() {
		return
	}
	scr.addRelease(dom.On(section, "click", func(ev js.Value) {
		target := ev.Get("target")
		if target.IsNull() || target.IsUndefined() {
			return
		}
		dataset := target.Get("dataset")
		if dataset.IsNull() || dataset.IsUndefined() {
			return
		}
		sectionAttr := dataset.Get("section")
		pageAttr := dataset.Get("page")
		if sectionAttr.IsUndefined() || pageAttr.IsUndefined() {
			return
		}
		sectionName := sectionAttr.String()
		pageNum, err := strconv.Atoi(pageAttr.String())
		if err != nil || pageNum < 1 {
			return
		}

		ctx := context.Background()

		switch sectionName {
		case "subs":
			go func() {
				if err := page.LoadSubsPage(ctx, pageNum); err != nil {
					sec := doc.Call("getElementById", "subs-section")
					if !sec.IsNull() && !sec.IsUndefined() {
						sec.Set("innerHTML", "<p>Error loading subscriptions: "+dom.Escape(err.Error())+"</p>")
					}
					return
				}
				sec := doc.Call("getElementById", "subs-section")
				if !sec.IsNull() && !sec.IsUndefined() {
					sec.Set("innerHTML", ui.RenderSubsSection(page.State()))
				}
			}()
		case "daily-events":
			go func() {
				if err := page.LoadDailyEventsPage(ctx, pageNum); err != nil {
					sec := doc.Call("getElementById", "daily-events-section")
					if !sec.IsNull() && !sec.IsUndefined() {
						sec.Set("innerHTML", "<p>Error loading daily events: "+dom.Escape(err.Error())+"</p>")
					}
					return
				}
				sec := doc.Call("getElementById", "daily-events-section")
				if !sec.IsNull() && !sec.IsUndefined() {
					sec.Set("innerHTML", ui.RenderDailyEventsSection(page.State()))
				}
			}()
		}
	}))
}

// runRenderMeSubscriptionsEdit is the entry point for the subscription editor
// screen. Reads initData once at mount, renders the skeleton immediately, loads
// the subscription list + active sources in a background goroutine, then wires
// event handlers. Must be called from a goroutine — never from the main goroutine.
func runRenderMeSubscriptionsEdit(client *apiclient.Client) {
	callWebAppIfDefined()

	initData := readInitData()

	screenCtx, cancelScreen := context.WithCancel(context.Background())
	doc := js.Global().Get("document")
	app := doc.Call("getElementById", "app")

	scr := mountScreen()
	scr.addRelease(cancelScreen)

	page := application.NewMeSubscriptionsEditPage(client, initData)

	// alive guards DOM writes against stale screens after navigation.
	alive := true
	scr.addRelease(func() { alive = false })

	redraw := func() {
		if !alive {
			return
		}
		app.Set("innerHTML", ui.RenderMeSubscriptionsEdit(page.State()))
	}

	// Render skeleton immediately.
	redraw()

	// Load data in background so the skeleton is visible right away.
	go func() {
		fetchCtx, fetchCancel := context.WithTimeout(screenCtx, 15*time.Second)
		defer fetchCancel()
		if err := page.LoadInitial(fetchCtx); err != nil {
			js.Global().Get("console").Call("warn", "edit LoadInitial:", err.Error())
		}
		redraw()
		bindMeSubsEditHandlers(screenCtx, doc, app, page, scr, &alive, redraw)
	}()
}

// bindMeSubsEditHandlers wires all event handlers for the subscription editor screen.
//
// All interactions are delegated to the stable #app container so that the listeners
// survive repeated innerHTML replacements performed by redraw(). Newly rendered
// child nodes are always inside #app, so delegated events bubble up correctly
// without requiring re-binding after each redraw.
//
// Back button (#me-edit-back): calls _wasm.renderMeSubscriptions() to navigate back.
// Source select (#me-edit-source): updates draft source name via SetDraftSource.
// Condition-type radios: update draft condition type via SetDraftConditionType; full
// re-render on change so the placeholder and help text update immediately.
// Value input (#me-edit-value): updates draft condition value via SetDraftConditionValue
// on every input event without re-rendering (preserves cursor position).
// Save (#me-edit-save): calls SaveDraft; re-renders on success or failure.
// Cancel/Clear (#me-edit-cancel): clears the draft by re-constructing the form area.
// Delete buttons (.me-edit-delete): reads data-id; calls window.Telegram.WebApp.showConfirm
// when available, else falls back to window.confirm; calls DeleteRow and re-renders on
// confirmation.
//
// ctx is the screen-lifetime context. alive is a pointer into runRenderMeSubscriptionsEdit's
// alive bool. redraw repaints the full app element from the current page state.
func bindMeSubsEditHandlers(
	ctx context.Context,
	_ js.Value,
	app js.Value,
	page *application.MeSubscriptionsEditPage,
	scr *screen,
	_ *bool,
	redraw func(),
) {
	// Delegated click handler on the stable #app container.
	// Every button/link inside the editor routes here via event bubbling.
	scr.addRelease(dom.On(app, "click", func(ev js.Value) {
		target := ev.Get("target")
		if target.IsNull() || target.IsUndefined() {
			return
		}
		id := target.Get("id").String()
		switch id {
		case "me-edit-back":
			// In the form view, Back returns to the list view; in the list
			// view, Back leaves the editor for the main Mini App screen.
			if page.State().ActiveView == application.EditViewForm {
				page.ShowListView()
				redraw()
			} else {
				js.Global().Get("_wasm").Call("renderMeSubscriptions")
			}
		case "me-edit-add":
			page.ShowFormView()
			redraw()
		case "me-edit-save":
			go func() {
				fetchCtx, fetchCancel := context.WithTimeout(ctx, 15*time.Second)
				defer fetchCancel()
				if err := page.SaveDraft(fetchCtx); err != nil {
					js.Global().Get("console").Call("warn", "save subscription:", err.Error())
				}
				redraw()
			}()
		case "me-edit-cancel":
			page.ClearDraft()
			redraw()
		case "me-edit-provider-trigger":
			page.OpenProviderPicker()
			redraw()
		case "me-edit-pair-trigger":
			page.OpenPairPicker()
			redraw()
		case "me-edit-picker-backdrop":
			page.ClosePickers()
			redraw()
		default:
			// Picker item click: dispatch by data-provider or data-source-name
			// before falling through to the delete-button path. closest() walks
			// up so child nodes inside the <li> still resolve correctly.
			item := target.Call("closest", ".me-edit-picker-item")
			if !item.IsNull() && !item.IsUndefined() {
				ds := item.Get("dataset")
				if pv := ds.Get("provider"); !pv.IsUndefined() {
					page.ChooseProvider(pv.String())
					redraw()
					return
				}
				if sn := ds.Get("sourceName"); !sn.IsUndefined() {
					page.ChoosePair(sn.String())
					redraw()
					return
				}
			}

			// Pagination prev/next: read data-kind and data-page; ignore the
			// click when the button is disabled (HTML attr already blocks the
			// browser, but JS may dispatch synthetic events).
			pageBtn := target.Call("closest", ".me-edit-picker-prev")
			if pageBtn.IsNull() || pageBtn.IsUndefined() {
				pageBtn = target.Call("closest", ".me-edit-picker-next")
			}
			if !pageBtn.IsNull() && !pageBtn.IsUndefined() {
				if pageBtn.Get("disabled").Bool() {
					return
				}
				ds := pageBtn.Get("dataset")
				kindVal := ds.Get("kind")
				pageVal := ds.Get("page")
				if kindVal.IsUndefined() || pageVal.IsUndefined() {
					return
				}
				pageNum, err := strconv.Atoi(pageVal.String())
				if err != nil || pageNum < 1 {
					return
				}
				switch kindVal.String() {
				case "provider":
					page.SetProviderPage(pageNum)
				case "pair":
					page.SetPairPage(pageNum)
				case "list":
					page.SetListPage(pageNum)
				}
				redraw()
				return
			}

			// Delete button: walk up to the nearest .me-edit-delete to handle
			// clicks on child elements inside the button.
			delBtn := target.Call("closest", ".me-edit-delete")
			if delBtn.IsNull() || delBtn.IsUndefined() {
				return
			}
			rowID := delBtn.Get("dataset").Get("id").String()
			if rowID == "" {
				return
			}

			confirmAndDelete := func(confirmed bool) {
				if !confirmed {
					return
				}
				go func() {
					fetchCtx, fetchCancel := context.WithTimeout(ctx, 15*time.Second)
					defer fetchCancel()
					if err := page.DeleteRow(fetchCtx, rowID); err != nil {
						js.Global().Get("console").Call("warn", "delete subscription:", err.Error())
					}
					redraw()
				}()
			}

			// Use Telegram WebApp.showConfirm when available (async/callback);
			// fall back to synchronous window.confirm.
			webApp := js.Global().Get("Telegram").Get("WebApp")
			if !webApp.IsUndefined() && !webApp.IsNull() {
				showConfirm := webApp.Get("showConfirm")
				if !showConfirm.IsUndefined() && !showConfirm.IsNull() {
					// js.FuncOf is Released inside the callback after it fires once
					// to avoid leaking the WASM function-table slot.
					var cb js.Func
					cb = js.FuncOf(func(_ js.Value, args []js.Value) any {
						cb.Release()
						confirmed := len(args) > 0 && args[0].Bool()
						confirmAndDelete(confirmed)
						return nil
					})
					webApp.Call("showConfirm", "Delete this subscription?", cb)
					return
				}
			}
			// Synchronous fallback.
			confirmed := js.Global().Call("confirm", "Delete this subscription?").Bool()
			confirmAndDelete(confirmed)
		}
	}))

	// Delegated change handler on the stable #app container.
	// Handles condition-type radio changes; the legacy source <select> is gone
	// (replaced by the picker overlays which fire click events, not change).
	scr.addRelease(dom.On(app, "change", func(ev js.Value) {
		t := ev.Get("target")
		if t.IsNull() || t.IsUndefined() {
			return
		}
		// Condition-type and direction radios are matched by name; they share
		// the same group-class but route to different controller methods.
		switch t.Get("name").String() {
		case "me-edit-cond-type":
			page.SetDraftConditionType(t.Get("value").String())
			redraw()
		case "me-edit-direction":
			page.SetDraftDirection(t.Get("value").String())
			redraw()
		}
	}))

	// Delegated input handler on the stable #app container. Three sites:
	//   1. #me-edit-value — condition-value keystrokes. No redraw; the value
	//      input does not feed any visible list.
	//   2. #me-edit-provider-search and #me-edit-pair-search — picker query
	//      keystrokes. A FULL redraw would recreate the search input element
	//      and lose caret position, so these update only the matching results
	//      slot (#me-edit-{provider,pair}-results-slot) and leave the input
	//      element alone.
	scr.addRelease(dom.On(app, "input", func(ev js.Value) {
		t := ev.Get("target")
		if t.IsNull() || t.IsUndefined() {
			return
		}
		switch t.Get("id").String() {
		case "me-edit-value":
			page.SetDraftConditionValue(t.Get("value").String())
		case "me-edit-provider-search":
			page.SetProviderQuery(t.Get("value").String())
			slot := js.Global().Get("document").Call("getElementById", "me-edit-provider-results-slot")
			if !slot.IsNull() && !slot.IsUndefined() {
				slot.Set("innerHTML", ui.RenderProviderResultsSlot(page.State()))
			}
		case "me-edit-pair-search":
			page.SetPairQuery(t.Get("value").String())
			slot := js.Global().Get("document").Call("getElementById", "me-edit-pair-results-slot")
			if !slot.IsNull() && !slot.IsUndefined() {
				slot.Set("innerHTML", ui.RenderPairResultsSlot(page.State()))
			}
		case "me-edit-list-search":
			page.SetListQuery(t.Get("value").String())
			slot := js.Global().Get("document").Call("getElementById", "me-edit-list-results-slot")
			if !slot.IsNull() && !slot.IsUndefined() {
				slot.Set("innerHTML", ui.RenderEditListResultsSlot(page.State()))
			}
		}
	}))
}
