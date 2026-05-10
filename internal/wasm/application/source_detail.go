package application

import (
	"context"
	"sort"
	"strings"

	"github.com/seilbekskindirov/monitor/internal/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

const (
	// SubsLimit is the page size for the subscriptions table, matching the JS
	// version's paginationHtml call at index.html:158.
	SubsLimit = 25
	// DailyEventsLimit is the page size for the daily events table.
	DailyEventsLimit = 25
)

// SourceDetailState holds all client-side state for the Source Detail screen.
// It is the single source of truth that the UI layer reads to render the page.
type SourceDetailState struct {
	Name   string
	Title  string
	Source api.SourceResponse

	Rates        []api.RateResponse
	RateFilter   string
	RateSortDesc bool

	Subs     []api.SubscriptionDetailResponse
	SubsPage int

	DailyEvents     []api.DailyEventResponse
	DailyEventsPage int
}

// VisibleRates returns the rate slice after applying the current filter and
// sort. Pure: no I/O, no DOM calls.
//
// Filter is a case-insensitive substring match on "<base>/<quote>". Sort is by
// Timestamp descending by default; empty Timestamp values sort as time.Time{}
// (zero) and therefore appear last in desc order and first in asc order.
func (s SourceDetailState) VisibleRates() []api.RateResponse {
	filterLower := strings.ToLower(s.RateFilter)
	out := make([]api.RateResponse, 0, len(s.Rates))
	for _, r := range s.Rates {
		if filterLower != "" {
			pair := strings.ToLower(r.BaseCurrency + "/" + r.QuoteCurrency)
			if !strings.Contains(pair, filterLower) {
				continue
			}
		}
		out = append(out, r)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ti := parseTime(out[i].Timestamp)
		tj := parseTime(out[j].Timestamp)
		if s.RateSortDesc {
			return ti.After(tj)
		}
		return ti.Before(tj)
	})
	return out
}

// SourceDetailPage is the page controller for the Source Detail screen. It
// owns SourceDetailState and exposes methods for every user action. It has no
// DOM dependencies and is testable as plain Go.
type SourceDetailPage struct {
	state  SourceDetailState
	client *apiclient.Client
}

// NewSourceDetailPage constructs a controller seeded with the initial fetches.
// Subscriptions and daily events arrive later via LoadSubsPage /
// LoadDailyEventsPage after the skeleton is in the DOM.
//
// Title lookup: sources is scanned for a SourceResponse whose Name matches
// name. When found, its Title field is used; when not found (or Title is
// empty), name is used as the fallback — matching the JS behaviour at
// index.html:104.
func NewSourceDetailPage(name string, sources []api.SourceResponse, rates []api.RateResponse, client *apiclient.Client) *SourceDetailPage {
	title := name
	var src api.SourceResponse
	for _, s := range sources {
		if s.Name == name {
			src = s
			if s.Title != "" {
				title = s.Title
			}
			break
		}
	}

	return &SourceDetailPage{
		state: SourceDetailState{
			Name:            name,
			Title:           title,
			Source:          src,
			Rates:           rates,
			RateSortDesc:    true,
			SubsPage:        1,
			DailyEventsPage: 1,
		},
		client: client,
	}
}

// State returns a copy of the current state for reading by the UI layer.
func (p *SourceDetailPage) State() SourceDetailState { return p.state }

// OnRateFilter updates the rate filter value. Pure: no I/O, no fetch.
func (p *SourceDetailPage) OnRateFilter(v string) SourceDetailState {
	p.state.RateFilter = v
	return p.state
}

// ToggleRateSort flips the rate sort direction between desc and asc.
func (p *SourceDetailPage) ToggleRateSort() SourceDetailState {
	p.state.RateSortDesc = !p.state.RateSortDesc
	return p.state
}

// LoadSubsPage fetches the given page of subscriptions for this source and
// replaces the internal slice. It also updates SubsPage on success.
func (p *SourceDetailPage) LoadSubsPage(ctx context.Context, page int) error {
	items, err := p.client.ListSubscriptions(ctx, p.state.Name, page)
	if err != nil {
		return err
	}
	p.state.Subs = items
	p.state.SubsPage = page
	return nil
}

// LoadDailyEventsPage fetches the given page of daily events for this source
// and replaces the internal slice. It also updates DailyEventsPage on success.
func (p *SourceDetailPage) LoadDailyEventsPage(ctx context.Context, page int) error {
	items, err := p.client.ListDailyEvents(ctx, p.state.Name, page)
	if err != nil {
		return err
	}
	p.state.DailyEvents = items
	p.state.DailyEventsPage = page
	return nil
}
