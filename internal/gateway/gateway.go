// Package gateway is the composition root for the HTTP layer. It wires the
// service and repository dependencies into a ready-to-serve *http.ServeMux.
package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prorochestvo/loginjector"
	appchart "github.com/seilbekskindirov/beacon/internal/application/chart"
	"github.com/seilbekskindirov/beacon/internal/application/service"
	appsub "github.com/seilbekskindirov/beacon/internal/application/subscription"
	appweather "github.com/seilbekskindirov/beacon/internal/application/weather"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
	"github.com/seilbekskindirov/beacon/internal/gateway/httpv1"
)

// WeatherGatewayDeps groups the weather-specific dependencies passed to NewGateway.
type WeatherGatewayDeps struct {
	// Service backs the caller's own weather reads: GET /api/v1/me/weather/cities
	// and GET /api/v1/me/weather/current.
	Service *appweather.Service
	// CityRepo is the weather city subscription repository.
	CityRepo meWeatherCityRepo
	// Geocoder is the geocoding provider for the city-search endpoint.
	Geocoder meWeatherGeocoder
}

// meProfileRepo is a pass-through interface for user-profile upserts.
type meProfileRepo interface {
	UpsertRateUserProfile(ctx context.Context, record *domain.RateUserProfile) error
}

// healthCheckAgent is a pass-through interface for the dependency-health aggregator.
// Nil is allowed; NewGateway forwards it to the router which forwards it to the
// HealthCheck handler. The handler returns 503 when the agent is not wired.
type healthCheckAgent interface {
	CheckUp(ctx context.Context) (healthy bool, report map[string]string)
}

// meWeatherCityRepo is a pass-through interface for the weather city subscription repository.
type meWeatherCityRepo interface {
	RetainWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
	ObtainWeatherUserCitiesByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.WeatherUserCity, error)
	ObtainWeatherUserCityByID(ctx context.Context, id string) (*domain.WeatherUserCity, error)
	RemoveWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
	RemoveWeatherUserCitiesByLocation(ctx context.Context, userType domain.UserType, userID, locationID string) error
}

// meWeatherGeocoder is a pass-through interface for the geocoding provider used
// by the city search endpoint. The method signature matches the handler layer's
// weatherGeocoder interface exactly.
type meWeatherGeocoder interface {
	Geocode(ctx context.Context, name string, count int) ([]dto.WeatherCitySearchItem, error)
}

// NewGateway builds the v1 HTTP mux with all routes registered, ready for
// http.ListenAndServe. subSvc backs the /api/v1/me/subscriptions family and
// chartSvc is required for GET /api/v1/me/rates/chart.
// healthAgent drives GET /health/check; when nil the endpoint returns 503.
// serverVersion and serverStart populate the "server" block in the health response.
// weather groups the weather-specific dependencies.
func NewGateway(
	srvRateRestApi *service.RateRestApi,
	botToken string,
	subSvc *appsub.Service,
	profileRepo meProfileRepo,
	chartSvc *appchart.Service,
	healthAgent healthCheckAgent,
	serverVersion string,
	serverStart time.Time,
	weather WeatherGatewayDeps,
) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	mux, err := httpv1.NewRouter(
		mux, srvRateRestApi, botToken, subSvc, profileRepo,
		chartSvc, healthAgent, serverVersion, serverStart,
		httpv1.WeatherGatewayDeps{
			Service:  weather.Service,
			CityRepo: weather.CityRepo,
			Geocoder: weather.Geocoder,
		},
	)
	if err != nil {
		err = errors.Join(err, loginjector.NewTraceError())
		return nil, err
	}
	return mux, nil
}
