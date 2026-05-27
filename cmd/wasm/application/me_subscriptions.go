package application

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// MeSubscriptionsPageSize is the default page size sent to /api/me/subscriptions.
const MeSubscriptionsPageSize = 10

// MeSubscriptionsPeriodWeek is the "week" period label for the chart toggle.
const MeSubscriptionsPeriodWeek = "week"

// MeSubscriptionsPeriodMonth is the "month" period label for the chart toggle.
const MeSubscriptionsPeriodMonth = "month"

// MeSubscriptionsPeriodYear is the "year" period label for the chart toggle.
const MeSubscriptionsPeriodYear = "year"

// MeSubscriptionsDefaultPeriod is the period selected on screen mount.
const MeSubscriptionsDefaultPeriod = MeSubscriptionsPeriodWeek

// authFailureSentinel is the error message produced by the apiclient when the
// server returns 401. The controller matches on this prefix to route to the
// auth-failure UX instead of the generic error UX.
const authFailureSentinel = "http 401"

// ChartState is the per-card chart-fetch state. The UI inspects fields in
// priority order: Error wins (renders an inline error glyph), then Loaded
// (renders the polyline), then Loading (renders a skeleton placeholder).
type ChartState struct {
	Loading bool
	Loaded  bool
	Error   error
	Points  []dto.ChartPointResponse
}

// MeSubscriptionsState is a read-only snapshot the UI layer consumes to render
// one of four possible states: loading-skeleton, card-list, empty-list, or
// error (auth-failure or generic).
type MeSubscriptionsState struct {
	Items    []dto.MeSubscriptionRow
	Total    int64
	Page     int
	PageSize int
	Query    string
	// Period is the currently selected chart period ("week", "month", "year").
	Period string
	// Charts holds the per-card chart-fetch state keyed by SourceName.
	Charts map[string]ChartState
	// Expanded holds the tap-to-expand state for each card.
	Expanded map[string]bool
	// AuthFailure is true when the server responded 401 (initData HMAC failed).
	// The UI renders the "open from bot" message and hides pagination.
	AuthFailure bool
	// LastError holds the most recent non-nil error. Nil means the last fetch
	// succeeded (or LoadInitial has not been called yet).
	LastError error
}

// MeSubscriptionsPage is the page controller for the Telegram Mini App
// subscriptions screen. It is pure Go with no syscall/js dependencies and
// is therefore testable under the host toolchain via make test.
//
// Concurrency note: Go-WASM runs on a single OS thread, so state mutations
// within a single goroutine are safe without a mutex. The debounce timer fires
// its callback on a new goroutine, but that goroutine only writes state and
// calls the onResult callback — it never reads from another goroutine
// concurrently. LoadChart is called from 10 goroutines concurrently in WASM
// (which is single-threaded), but the host test toolchain runs real goroutines;
// tests must call LoadChart sequentially to avoid races. If the project ever
// moves to multi-threaded WASM, add a sync.Mutex around state reads/writes.
type MeSubscriptionsPage struct {
	client   *apiclient.Client
	initData string
	state    MeSubscriptionsState

	// generation is incremented atomically on every fetchAndStore and SetPeriod
	// call so chart goroutines launched for a prior generation can detect staleness
	// and drop their result without mutating state.
	generation atomic.Int64

	// debounce holds the pending search timer. Stop and reset on every
	// OnSearch call so only the final keystroke triggers a fetch.
	debounce *time.Timer
}

// NewMeSubscriptionsPage constructs a controller. initData is the Telegram
// WebApp initData string read once at WASM boot from window.Telegram.WebApp;
// it is forwarded unchanged on every MeSubscriptions call.
// pageSize controls how many rows the server is asked for per request.
func NewMeSubscriptionsPage(client *apiclient.Client, initData string, pageSize int) *MeSubscriptionsPage {
	if pageSize <= 0 {
		pageSize = MeSubscriptionsPageSize
	}
	return &MeSubscriptionsPage{
		client:   client,
		initData: initData,
		state: MeSubscriptionsState{
			Page:     1,
			PageSize: pageSize,
			Period:   MeSubscriptionsDefaultPeriod,
			Charts:   map[string]ChartState{},
			Expanded: map[string]bool{},
		},
	}
}

// State returns a snapshot of the current controller state. The caller must
// not mutate the returned slice.
func (p *MeSubscriptionsPage) State() MeSubscriptionsState { return p.state }

// LoadInitial fetches the first page of subscriptions. It is called once at
// screen mount before any user interaction.
func (p *MeSubscriptionsPage) LoadInitial(ctx context.Context) error {
	p.state.Page = 1
	return p.fetchAndStore(ctx)
}

// NextPage increments the page counter and fetches the next page.
// It mirrors the JS "next" button handler: there is no upper-bound guard
// in the controller — the caller is responsible for not offering the Next
// button when the current page is already the last one (i.e. when
// len(Items) < PageSize or via Total math).
func (p *MeSubscriptionsPage) NextPage(ctx context.Context) error {
	p.state.Page++
	return p.fetchAndStore(ctx)
}

// PrevPage decrements the page counter and fetches the previous page.
// It mirrors the JS "prev" button handler: page is not decremented below 1.
func (p *MeSubscriptionsPage) PrevPage(ctx context.Context) error {
	if p.state.Page <= 1 {
		return nil
	}
	p.state.Page--
	return p.fetchAndStore(ctx)
}

// OnSearch stores the new query, resets to page 1, and schedules a fetch
// 250 ms after the last call. If a previous timer is still pending it is
// cancelled so only the final keystroke fires a network request.
//
// The returned channel receives the fetch error (nil on success) exactly once,
// after the debounced fetch has settled. The caller (cmd/wasm/main.go) listens
// on this channel to know when to re-render the section and to log any error.
//
// Design choice: channel over callback. A channel lets the caller select{}
// it alongside other signals (e.g. context cancellation) without the
// controller needing to know about the DOM. Each OnSearch call returns a
// fresh channel; the caller should discard the channel from the previous
// call once it starts listening on the new one.
func (p *MeSubscriptionsPage) OnSearch(q string) <-chan error {
	p.state.Query = q
	p.state.Page = 1

	done := make(chan error, 1)

	if p.debounce != nil {
		p.debounce.Stop()
	}
	p.debounce = time.AfterFunc(250*time.Millisecond, func() {
		done <- p.fetchAndStore(context.Background())
	})

	return done
}

// SetPeriod updates the page-level period and clears all per-card chart and
// expand state when the period actually changes. Returns a PublicError with
// message "Invalid period." when period is not one of MeSubscriptionsPeriodWeek,
// MeSubscriptionsPeriodMonth, or MeSubscriptionsPeriodYear. Clearing state on
// a new period invalidates any in-flight LoadChart goroutines from the previous
// period — they will detect the staleness via SnapshotGeneration and drop their
// results without mutating state.
func (p *MeSubscriptionsPage) SetPeriod(_ context.Context, period string) error {
	switch period {
	case MeSubscriptionsPeriodWeek, MeSubscriptionsPeriodMonth, MeSubscriptionsPeriodYear:
	default:
		return internal.NewPublicError("Invalid period.")
	}
	if p.state.Period == period {
		return nil
	}
	p.state.Period = period
	p.state.Charts = map[string]ChartState{}
	p.state.Expanded = map[string]bool{}
	p.generation.Add(1)
	return nil
}

// LoadChart fetches chart data for the named source using the current period.
// gen must be the value returned by SnapshotGeneration at goroutine launch; if
// the generation has since changed (period switch or page change), the result
// is dropped on the floor and nil is returned — the state is intentionally left
// untouched so the new generation's fetch can write its own result.
//
// On success, Charts[name] is set to ChartState{Loaded: true, Points: …}.
// On fetch failure, Charts[name] is set to ChartState{Error: …} and the error
// is returned.
//
// Concurrency: in Go-WASM this is safe without a mutex because the runtime is
// single-threaded. The host test toolchain runs real goroutines; call this
// method sequentially in tests to avoid races. If WASM ever becomes
// multi-threaded, add a sync.Mutex around all p.state reads/writes.
func (p *MeSubscriptionsPage) LoadChart(ctx context.Context, name string, gen int64) error {
	if p.generation.Load() != gen {
		return nil
	}
	p.state.Charts[name] = ChartState{Loading: true}
	points, err := p.client.RatesChart(ctx, name, p.state.Period)
	if p.generation.Load() != gen {
		// A period/page change arrived while the fetch was in flight; discard.
		return nil
	}
	if err != nil {
		p.state.Charts[name] = ChartState{Error: err}
		return err
	}
	p.state.Charts[name] = ChartState{Loaded: true, Points: points}
	return nil
}

// SnapshotGeneration returns the current generation counter. Callers capture
// this value before launching a LoadChart goroutine and pass it back in so
// stale results from superseded generations are discarded.
func (p *MeSubscriptionsPage) SnapshotGeneration() int64 {
	return p.generation.Load()
}

// ToggleExpand flips the expand state for the named card. Cards are collapsed
// by default (absent key == false); the first call sets them to true.
func (p *MeSubscriptionsPage) ToggleExpand(name string) {
	p.state.Expanded[name] = !p.state.Expanded[name]
}

// fetchAndStore calls the API client, stores the result in state, and returns
// the error (also stored in state for UI inspection). A 401 error sets
// AuthFailure=true so the UI can show the "open from bot" message.
// It increments the generation counter to invalidate any in-flight chart
// goroutines from the previous page, and clears chart and expand state.
func (p *MeSubscriptionsPage) fetchAndStore(ctx context.Context) error {
	p.generation.Add(1)
	p.state.Charts = map[string]ChartState{}
	p.state.Expanded = map[string]bool{}
	resp, err := p.client.MeSubscriptions(ctx, p.initData, p.state.Page, p.state.PageSize, p.state.Query)
	if err != nil {
		p.state.Items = nil
		p.state.Total = 0
		p.state.AuthFailure = strings.Contains(err.Error(), authFailureSentinel)
		p.state.LastError = err
		return err
	}
	p.state.Items = resp.Items
	p.state.Total = resp.Total
	p.state.AuthFailure = false
	p.state.LastError = nil
	return nil
}
