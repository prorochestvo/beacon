package application

import (
	"context"
	"strings"

	"github.com/seilbekskindirov/beacon/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/beacon/internal/dto"
)

// WeatherCurrentState is the read-only snapshot consumed by the current-weather UI.
//
// Concurrency note: WASM runs on a single OS thread, so state mutations are safe
// without a mutex. If the project ever moves to multi-threaded WASM, add a mutex.
type WeatherCurrentState struct {
	// Items is the list of per-city weather observations returned by the server.
	Items []dto.WeatherCurrentItem
	// Loading is true while the initial load is in-flight.
	Loading bool
	// LoadError is the most recent non-nil load error; nil on success.
	LoadError error
	// AuthFailure is true when the authenticated call received a 401 response.
	AuthFailure bool
}

// MeWeatherCurrentPage is the page controller for the on-demand current-weather
// screen. Pure Go, no syscall/js dependencies, testable under the host toolchain.
type MeWeatherCurrentPage struct {
	client   *apiclient.Client
	initData string
	state    WeatherCurrentState
}

// NewMeWeatherCurrentPage constructs a controller. initData is forwarded
// unchanged on every authenticated API call.
func NewMeWeatherCurrentPage(client *apiclient.Client, initData string) *MeWeatherCurrentPage {
	return &MeWeatherCurrentPage{
		client:   client,
		initData: initData,
	}
}

// State returns a snapshot of the current controller state.
// The caller must not mutate the returned slices.
func (p *MeWeatherCurrentPage) State() WeatherCurrentState { return p.state }

// Load fetches the latest weather observations for the caller's subscribed cities.
// Sets AuthFailure on 401; stores the error in LoadError on any failure.
func (p *MeWeatherCurrentPage) Load(ctx context.Context) error {
	p.state.Loading = true
	defer func() { p.state.Loading = false }()
	p.state.LoadError = nil

	resp, err := p.client.MeWeatherCurrent(ctx, p.initData)
	if err != nil {
		if strings.Contains(err.Error(), AuthFailureSentinel) {
			p.state.AuthFailure = true
		}
		p.state.LoadError = err
		return err
	}

	p.state.Items = resp.Items
	if p.state.Items == nil {
		p.state.Items = []dto.WeatherCurrentItem{}
	}
	return nil
}
