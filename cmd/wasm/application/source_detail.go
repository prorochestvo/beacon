package application

import (
	"context"
	"sort"
	"strings"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

const (
	// SubsLimit is the page size for the subscriptions table.
	SubsLimit = 25
	// DailyEventsLimit is the page size for the daily events table.
	DailyEventsLimit = 25
)

// SourceDetailState holds all client-side state for the Source Detail screen —
// the single source of truth the UI layer reads to render the page.
type SourceDetailState struct {
	Name   string
	Title  string
	Source dto.SourceResponse

	Rates        []dto.RateResponse
	RateFilter   string
	RateSortDesc bool

	Subs     []dto.SubscriptionDetailResponse
	SubsPage int

	DailyEvents     []dto.DailyEventResponse
	DailyEventsPage int
}

// VisibleRates returns the rate slice after the current filter and sort. Pure:
// no I/O, no DOM calls.
//
// Filter is a case-insensitive substring match on "<base>/<quote>". Sort is by
// Timestamp, descending by default; empty Timestamp sorts as time.Time{} (zero),
// so it appears last in desc order and first in asc.
func (s SourceDetailState) VisibleRates() []dto.RateResponse {
	filterLower := strings.ToLower(s.RateFilter)
	out := make([]dto.RateResponse, 0, len(s.Rates))
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

// SourceDetailPage is the page controller for the Source Detail screen. It owns
// SourceDetailState and exposes a method per user action. No DOM dependencies;
// testable as plain Go.
type SourceDetailPage struct {
	state  SourceDetailState
	client *apiclient.Client
}

// NewSourceDetailPage constructs a controller seeded with the initial fetches.
// Subscriptions and daily events arrive later via LoadSubsPage /
// LoadDailyEventsPage after the skeleton is in the DOM.
//
// Title lookup: sources is scanned for a SourceResponse whose Name matches name;
// its Title is used when found and non-empty, otherwise name is the fallback.
func NewSourceDetailPage(name string, sources []dto.SourceResponse, rates []dto.RateResponse, client *apiclient.Client) *SourceDetailPage {
	title := name
	var src dto.SourceResponse
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

// LoadSubsPage fetches the given page of subscriptions for this source,
// replaces the internal slice, and updates SubsPage on success.
func (p *SourceDetailPage) LoadSubsPage(ctx context.Context, page int) error {
	items, err := p.client.ListSubscriptions(ctx, p.state.Name, page)
	if err != nil {
		return err
	}
	p.state.Subs = items
	p.state.SubsPage = page
	return nil
}

// LoadDailyEventsPage fetches the given page of daily events for this source,
// replaces the internal slice, and updates DailyEventsPage on success.
func (p *SourceDetailPage) LoadDailyEventsPage(ctx context.Context, page int) error {
	items, err := p.client.ListDailyEvents(ctx, p.state.Name, page)
	if err != nil {
		return err
	}
	p.state.DailyEvents = items
	p.state.DailyEventsPage = page
	return nil
}
