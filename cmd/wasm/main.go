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
// search bar + skeleton, loads the first page of subscriptions, fans out one
// chart-fetch goroutine per visible card, and binds all event handlers.
// Must be called from a goroutine.
func runRenderMeSubscriptions(client *apiclient.Client) {
	callWebAppIfDefined()

	initData := readInitData()

	ctx := context.Background()
	doc := js.Global().Get("document")
	app := doc.Call("getElementById", "app")

	app.Set("innerHTML", "<p>Loading…</p>")

	scr := mountScreen()
	page := application.NewMeSubscriptionsPage(client, initData, 10)

	// Render the skeleton (search bar + period toggle + loading placeholder)
	// immediately so the user sees a responsive UI before the first round-trip.
	app.Set("innerHTML", ui.RenderMeSubscriptions(page.State()))

	redrawList := func() {
		listDiv := doc.Call("getElementById", "me-subs-list")
		if listDiv.IsNull() || listDiv.IsUndefined() {
			return
		}
		listDiv.Set("innerHTML", ui.RenderMeSubsList(page.State()))
		paginationDiv := doc.Call("getElementById", "me-subs-pagination")
		if !paginationDiv.IsNull() && !paginationDiv.IsUndefined() {
			paginationDiv.Set("innerHTML", ui.RenderMeSubsPagination(page.State()))
		}
	}

	// Load the first page synchronously in this goroutine. Errors are stored
	// on the page state and rendered by redrawList.
	if err := page.LoadInitial(ctx); err != nil {
		js.Global().Get("console").Call("warn", "me-subs LoadInitial:", err.Error())
	}
	redrawList()

	// fanOutChartFetches issues one goroutine per visible card. Each goroutine
	// calls LoadChart and, on completion, redraws only its own chart slot so
	// it does not trample the search input focus or other in-flight slot updates.
	fanOutChartFetches := func() {
		gen := page.SnapshotGeneration()
		for _, item := range page.State().Items {
			name := item.SourceName
			go func() {
				if err := page.LoadChart(ctx, name, gen); err != nil {
					js.Global().Get("console").Call("warn",
						"chart fetch failed for "+name+":", err.Error())
				}
				slotID := "card-chart-" + name
				slot := doc.Call("getElementById", slotID)
				if !slot.IsNull() && !slot.IsUndefined() {
					slot.Set("innerHTML", ui.RenderMeSubCardChartSlot(page.State(), name))
				}
			}()
		}
	}

	fanOutChartFetches()

	bindMeSubsHandlers(doc, page, scr, redrawList, fanOutChartFetches)
}

// bindMeSubsHandlers wires all event handlers for the Mini App screen.
//
// Search: oninput on #me-search dispatches through OnSearch and listens on the
// returned channel to know when the debounced fetch has settled. A new goroutine
// is started for each search so the JS event loop is never blocked. After the
// list settles a new chart fanout is issued for the visible cards.
//
// Period toggle: a click on #me-period-toggle reads data-period and calls
// page.SetPeriod, then redraws the list and fans out fresh chart fetches. A
// PublicError (invalid period value) is logged as console.warn and ignored.
//
// Expand: a delegated click on #me-subs-list checks whether the target is
// inside a .card-chart-slot via closest(). If so it reads data-source-name from
// the slot and calls page.ToggleExpand, then updates only that slot's innerHTML.
//
// Pagination: a delegated click on the #app div reads data-section and data-page
// to dispatch NextPage/PrevPage, followed by redrawList and a chart fanout.
func bindMeSubsHandlers(doc js.Value, page *application.MeSubscriptionsPage, scr *screen, redrawList, fanOutChartFetches func()) {
	searchEl := doc.Call("getElementById", "me-search")
	if !searchEl.IsNull() && !searchEl.IsUndefined() {
		scr.addRelease(dom.On(searchEl, "input", func(ev js.Value) {
			q := ev.Get("target").Get("value").String()
			done := page.OnSearch(q)
			go func() {
				if err := <-done; err != nil {
					js.Global().Get("console").Call("warn", "me-subs search:", err.Error())
				}
				redrawList()
				fanOutChartFetches()
			}()
		}))
	}

	periodToggle := doc.Call("getElementById", "me-period-toggle")
	if !periodToggle.IsNull() && !periodToggle.IsUndefined() {
		scr.addRelease(dom.On(periodToggle, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			dataset := target.Get("dataset")
			if dataset.IsNull() || dataset.IsUndefined() {
				return
			}
			periodAttr := dataset.Get("period")
			if periodAttr.IsUndefined() || periodAttr.IsNull() {
				return
			}
			period := periodAttr.String()
			ctx := context.Background()
			if err := page.SetPeriod(ctx, period); err != nil {
				js.Global().Get("console").Call("warn", "me-subs SetPeriod:", err.Error())
				return
			}
			redrawList()
			fanOutChartFetches()
		}))
	}

	listDiv := doc.Call("getElementById", "me-subs-list")
	if !listDiv.IsNull() && !listDiv.IsUndefined() {
		scr.addRelease(dom.On(listDiv, "click", func(ev js.Value) {
			target := ev.Get("target")
			if target.IsNull() || target.IsUndefined() {
				return
			}
			// Find the nearest .card-chart-slot ancestor (inclusive of target).
			slot := target.Call("closest", ".card-chart-slot")
			if slot.IsNull() || slot.IsUndefined() {
				return
			}
			nameVal := slot.Get("dataset").Get("sourceName")
			if nameVal.IsNull() || nameVal.IsUndefined() {
				return
			}
			name := nameVal.String()
			if name == "" {
				return
			}
			page.ToggleExpand(name)
			slotID := "card-chart-" + name
			slotEl := doc.Call("getElementById", slotID)
			if !slotEl.IsNull() && !slotEl.IsUndefined() {
				slotEl.Set("innerHTML", ui.RenderMeSubCardChartSlot(page.State(), name))
			}
		}))
	}

	appEl := doc.Call("getElementById", "app")
	if !appEl.IsNull() && !appEl.IsUndefined() {
		scr.addRelease(dom.On(appEl, "click", func(ev js.Value) {
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
			if sectionAttr.String() != "me-subs" {
				return
			}
			pageNum, err := strconv.Atoi(pageAttr.String())
			if err != nil || pageNum < 1 {
				return
			}
			ctx := context.Background()
			currentPage := page.State().Page
			go func() {
				if pageNum > currentPage {
					if err := page.NextPage(ctx); err != nil {
						js.Global().Get("console").Call("error", "me-subs NextPage:", err.Error())
					}
				} else {
					if err := page.PrevPage(ctx); err != nil {
						js.Global().Get("console").Call("error", "me-subs PrevPage:", err.Error())
					}
				}
				redrawList()
				fanOutChartFetches()
			}()
		}))
	}
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
