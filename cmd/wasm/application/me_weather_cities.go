package application

import (
	"context"
	"strings"

	"github.com/seilbekskindirov/beacon/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// WeatherCitiesState is the read-only snapshot consumed by the weather-city UI.
//
// Concurrency note: WASM runs on a single OS thread, so state mutations are safe
// without a mutex. If the project ever moves to multi-threaded WASM, add a mutex.
type WeatherCitiesState struct {
	// Cities is the caller's current saved city subscription list.
	Cities []dto.WeatherCityRow
	// Loading is true while the initial city list is loading.
	Loading bool
	// LoadError is the most recent non-nil load error; nil on success.
	LoadError error
	// AuthFailure is true when any authenticated call received a 401 response.
	AuthFailure bool

	// SearchQuery is the current text input value driving geocoding.
	SearchQuery string
	// SearchResults holds the most recent geocoding response.
	SearchResults []dto.WeatherCitySearchItem
	// SearchLoading is true while a geocoding request is in-flight.
	SearchLoading bool
	// SearchError is the most recent non-nil geocoding error; nil on success.
	SearchError error

	// Selected is the city item chosen from the search results but not yet saved.
	// Nil when no item is selected.
	Selected *dto.WeatherCitySearchItem
	// SaveError is the most recent non-nil POST error; nil on success.
	SaveError error
}

// MeWeatherCitiesPage is the page controller for the city weather subscription
// screen. Pure Go, no syscall/js dependencies, testable under the host toolchain.
type MeWeatherCitiesPage struct {
	client   *apiclient.Client
	initData string
	state    WeatherCitiesState
}

// NewMeWeatherCitiesPage constructs a controller. initData is forwarded
// unchanged on every authenticated API call.
func NewMeWeatherCitiesPage(client *apiclient.Client, initData string) *MeWeatherCitiesPage {
	return &MeWeatherCitiesPage{
		client:   client,
		initData: initData,
	}
}

// State returns a snapshot of the current controller state.
// The caller must not mutate the returned slices.
func (p *MeWeatherCitiesPage) State() WeatherCitiesState { return p.state }

// LoadCities fetches the caller's saved city list. Sets AuthFailure on 401.
func (p *MeWeatherCitiesPage) LoadCities(ctx context.Context) error {
	p.state.Loading = true
	defer func() { p.state.Loading = false }()
	p.state.LoadError = nil

	resp, err := p.client.MeWeatherCities(ctx, p.initData)
	if err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		p.state.LoadError = err
		return err
	}

	p.state.Cities = resp.Items
	if p.state.Cities == nil {
		p.state.Cities = []dto.WeatherCityRow{}
	}
	return nil
}

// SearchCities calls the geocoding endpoint for the given query. Intended to
// be called from a debounced input handler so the network call is not made on
// every keystroke. Empty query clears SearchResults without making a call.
func (p *MeWeatherCitiesPage) SearchCities(ctx context.Context, q string) error {
	p.state.SearchQuery = q
	p.state.SearchError = nil
	p.state.Selected = nil

	q = strings.TrimSpace(q)
	if q == "" {
		p.state.SearchResults = nil
		return nil
	}

	p.state.SearchLoading = true
	defer func() { p.state.SearchLoading = false }()

	resp, err := p.client.MeWeatherCitiesSearch(ctx, p.initData, q)
	if err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		p.state.SearchError = err
		return err
	}

	p.state.SearchResults = resp.Items
	if p.state.SearchResults == nil {
		p.state.SearchResults = []dto.WeatherCitySearchItem{}
	}
	return nil
}

// SelectSearchResult marks the item at index i in SearchResults as the pending
// city to save. No-op when i is out of bounds.
func (p *MeWeatherCitiesPage) SelectSearchResult(i int) {
	if i < 0 || i >= len(p.state.SearchResults) {
		return
	}
	item := p.state.SearchResults[i]
	p.state.Selected = &item
	p.state.SaveError = nil
}

// SaveSelected POSTs the currently selected search result as a new city
// subscription. On success it clears the search form and reloads the saved
// city list. Sets AuthFailure on 401.
func (p *MeWeatherCitiesPage) SaveSelected(ctx context.Context) error {
	if p.state.Selected == nil {
		return nil
	}
	p.state.SaveError = nil

	body := dto.WeatherCityCreateRequest{
		LocationID:  p.state.Selected.LocationID,
		DisplayName: p.state.Selected.DisplayName,
		Latitude:    p.state.Selected.Latitude,
		Longitude:   p.state.Selected.Longitude,
		Timezone:    p.state.Selected.Timezone,
		Country:     p.state.Selected.Country,
		Admin1:      p.state.Selected.Admin1,
	}

	if _, err := p.client.MeWeatherCityCreate(ctx, p.initData, body); err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		p.state.SaveError = err
		return err
	}

	// Clear the search form on success so the user sees the updated list.
	p.state.SearchQuery = ""
	p.state.SearchResults = nil
	p.state.Selected = nil

	return p.LoadCities(ctx)
}

// DeleteCity removes the city with the given id and reloads the list on success.
// Sets AuthFailure on 401.
func (p *MeWeatherCitiesPage) DeleteCity(ctx context.Context, id string) error {
	if err := p.client.MeWeatherCityDelete(ctx, p.initData, id); err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		return err
	}
	return p.LoadCities(ctx)
}

// ClearSearch resets the search form to its initial empty state.
func (p *MeWeatherCitiesPage) ClearSearch() {
	p.state.SearchQuery = ""
	p.state.SearchResults = nil
	p.state.SearchError = nil
	p.state.Selected = nil
	p.state.SaveError = nil
}
