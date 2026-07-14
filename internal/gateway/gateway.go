// Package gateway is the composition root for the HTTP layer. It wires the
// service and repository dependencies into a ready-to-serve *http.ServeMux.
package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	appchart "github.com/seilbekskindirov/beacon/internal/application/chart"
	"github.com/seilbekskindirov/beacon/internal/application/service"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
	"github.com/seilbekskindirov/beacon/internal/gateway/httpV1"
)

// WeatherGatewayDeps groups the weather-specific dependencies passed to NewGateway.
// Each field is nil-safe: the corresponding endpoints return 503 when a dep is absent.
type WeatherGatewayDeps struct {
	// CityRepo is the weather city subscription repository.
	CityRepo meWeatherCityRepo
	// Geocoder is the geocoding provider for the city-search endpoint.
	Geocoder meWeatherGeocoder
	// ObsRepo is the weather observation repository for the on-demand
	// current-weather endpoint (GET /api/me/weather/current).
	ObsRepo meWeatherObsRepo
}

// NewGateway builds the v1 HTTP mux with all routes registered, ready for
// http.ListenAndServe. chartSvc is required for GET /api/me/rates/chart.
// healthAgent drives GET /health/check; when nil the endpoint returns 503.
// serverVersion and serverStart populate the "server" block in the health response.
// weather groups the weather-specific dependencies; each is nil-safe — the
// corresponding endpoints return 503 when a dep is not wired.
func NewGateway(
	srvRateRestApi *service.RateRestApi,
	botToken string,
	subRepo meSubscriptionRepo,
	sourceRepo meSourceRepo,
	rateValueRepo meRateValueRepo,
	profileRepo meProfileRepo,
	chartSvc *appchart.Service,
	healthAgent healthCheckAgent,
	serverVersion string,
	serverStart time.Time,
	weather WeatherGatewayDeps,
) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	mux, err := httpV1.NewRouter(
		mux, srvRateRestApi, botToken, subRepo, sourceRepo, rateValueRepo, profileRepo,
		chartSvc, healthAgent, serverVersion, serverStart,
		httpV1.WeatherGatewayDeps{
			CityRepo: weather.CityRepo,
			Geocoder: weather.Geocoder,
			ObsRepo:  weather.ObsRepo,
		},
	)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	return mux, nil
}

// meSubscriptionRepo is a pass-through interface from the concrete repository layer.
type meSubscriptionRepo interface {
	ObtainRateUserSubscriptionsByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error)
	ObtainRateUserSubscriptionByID(ctx context.Context, id string) (*domain.RateUserSubscription, error)
	RetainRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error
	RemoveRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error
}

// meSourceRepo is a pass-through interface for source look-ups.
type meSourceRepo interface {
	ObtainRateSourceByName(ctx context.Context, name string) (*domain.RateSource, error)
	ObtainRateSourcesByNames(ctx context.Context, names []string) (map[string]domain.RateSource, error)
}

// meRateValueRepo is a pass-through interface for rate value look-ups.
type meRateValueRepo interface {
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, limit int64) ([]domain.RateValue, error)
	ObtainLatestRateValuesBySourceNames(ctx context.Context, names []string) (map[string]domain.RateValue, error)
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
}

// meWeatherGeocoder is a pass-through interface for the geocoding provider used
// by the city search endpoint. The method signature matches the handler layer's
// weatherGeocoder interface exactly.
type meWeatherGeocoder interface {
	Geocode(ctx context.Context, name string, count int) ([]dto.WeatherCitySearchItem, error)
}

// meWeatherObsRepo is a pass-through interface for the weather observation
// repository used by the on-demand current-weather endpoint.
type meWeatherObsRepo interface {
	ObtainLatestObservation(ctx context.Context, locationID, provider string) (*domain.WeatherObservation, error)
}
