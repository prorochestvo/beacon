package application

import (
	"context"
	"strings"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// SourcesState holds all client-side state for the Sources List screen — the
// single source of truth the UI layer reads to render the page.
type SourcesState struct {
	All          []dto.SourceResponse
	Stats        dto.StatsResponse
	FilterTitle  string
	FilterPair   string
	FilterStatus string // "all" | "ok" | "error"
	FilterActive string // "all" | "yes" | "no"
	SortDesc     bool
}

// Visible returns the source slice after filter+sort. Pure: no I/O, no DOM.
func (s SourcesState) Visible() []dto.SourceResponse {
	out := make([]dto.SourceResponse, 0, len(s.All))
	titleLower := strings.ToLower(s.FilterTitle)
	pairLower := strings.ToLower(s.FilterPair)

	for _, src := range s.All {
		if titleLower != "" {
			if !strings.Contains(strings.ToLower(src.Title), titleLower) &&
				!strings.Contains(strings.ToLower(src.Name), titleLower) {
				continue
			}
		}
		if pairLower != "" {
			pair := strings.ToLower(src.BaseCurrency + "/" + src.QuoteCurrency)
			if !strings.Contains(pair, pairLower) {
				continue
			}
		}
		if s.FilterStatus == "ok" && !src.LastSuccess {
			continue
		}
		if s.FilterStatus == "error" && src.LastSuccess {
			continue
		}
		if s.FilterActive == "yes" && !src.Active {
			continue
		}
		if s.FilterActive == "no" && src.Active {
			continue
		}
		out = append(out, src)
	}

	sortSourcesByLastRun(out, s.SortDesc)
	return out
}

// SourcesPage is the page controller for the Sources List screen. It owns
// SourcesState and exposes a method per user action. No DOM dependencies;
// testable as plain Go.
type SourcesPage struct {
	state  SourcesState
	client *apiclient.Client
}

// NewSourcesPage constructs a controller seeded with the given data. The
// client is used only by ToggleActive (network round-trip + re-fetch).
func NewSourcesPage(sources []dto.SourceResponse, stats dto.StatsResponse, client *apiclient.Client) *SourcesPage {
	return &SourcesPage{
		state: SourcesState{
			All:          sources,
			Stats:        stats,
			FilterStatus: "all",
			FilterActive: "all",
			SortDesc:     true,
		},
		client: client,
	}
}

// State returns a copy of the current state for reading by the UI layer.
func (p *SourcesPage) State() SourcesState { return p.state }

// OnFilterTitle updates the title/name filter and returns the new state.
func (p *SourcesPage) OnFilterTitle(v string) SourcesState {
	p.state.FilterTitle = v
	return p.state
}

// OnFilterPair updates the currency-pair filter and returns the new state.
func (p *SourcesPage) OnFilterPair(v string) SourcesState {
	p.state.FilterPair = v
	return p.state
}

// OnFilterStatus sets the status filter ("all", "ok", or "error") and returns
// the new state.
func (p *SourcesPage) OnFilterStatus(v string) SourcesState {
	p.state.FilterStatus = v
	return p.state
}

// OnFilterActive sets the active filter ("all", "yes", or "no") and returns
// the new state.
func (p *SourcesPage) OnFilterActive(v string) SourcesState {
	p.state.FilterActive = v
	return p.state
}

// ToggleSort flips the sort direction and returns the new state.
func (p *SourcesPage) ToggleSort() SourcesState {
	p.state.SortDesc = !p.state.SortDesc
	return p.state
}

// ToggleActive PATCHes the source's active flag and re-fetches the list.
// On success the internal All slice is replaced and the new state is returned.
func (p *SourcesPage) ToggleActive(ctx context.Context, name string, active bool) (SourcesState, error) {
	if err := p.client.SetSourceActive(ctx, name, active); err != nil {
		return p.state, err
	}
	updated, err := p.client.ListSources(ctx, 100)
	if err != nil {
		return p.state, err
	}
	p.state.All = updated
	return p.state, nil
}
