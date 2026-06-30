// Package httpV1 wires the v1 HTTP handlers onto the provided ServeMux.
package httpV1

import (
	"context"
	"net/http"
	"time"

	appchart "github.com/seilbekskindirov/beacon/internal/application/chart"
	"github.com/seilbekskindirov/beacon/internal/application/service"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
	v1 "github.com/seilbekskindirov/beacon/internal/gateway/httpV1/handlers"
	"github.com/seilbekskindirov/beacon/internal/gateway/httpV1/routes"
)

// WeatherGatewayDeps groups the weather-specific dependencies threaded into the
// router so the constructor signature does not grow one positional parameter per
// weather feature. Fields are added here as new weather endpoints need them.
type WeatherGatewayDeps struct {
	// CityRepo is the weather city subscription repository.
	CityRepo meWeatherCityRepo
	// Geocoder is the geocoding provider for the city-search endpoint.
	Geocoder weatherGeocoder
	// ObsRepo is the weather observation repository for the on-demand current-weather endpoint.
	ObsRepo meWeatherObsRepo
}

// NewRouter registers all v1 HTTP routes on mux and returns it.
func NewRouter(
	mux *http.ServeMux,
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
	h, err := v1.NewHandler(srvRateRestApi, botToken, subRepo, sourceRepo, rateValueRepo, profileRepo, chartSvc, healthAgent, serverVersion, serverStart)
	if err != nil {
		return nil, err
	}
	h.WithWeatherDeps(weather.CityRepo, weather.Geocoder)
	h.WithWeatherObsRepo(weather.ObsRepo)

	// MeSubscriptionsRaw must be registered before MeSubscriptions so Go 1.22+
	// ServeMux longest-path matching selects the more specific route.
	mux.HandleFunc("GET "+routes.PublicRatesChart, h.GetPublicRatesChart)
	mux.HandleFunc("GET "+routes.MeSubscriptionsRaw, h.ListMeSubscriptionsRaw)
	mux.HandleFunc("GET "+routes.MeSubscriptions, h.ListMeSubscriptions)
	mux.HandleFunc("POST "+routes.MeSubscriptions, h.CreateMeSubscription)
	mux.HandleFunc("PATCH "+routes.MeSubscriptionByID, h.UpdateMeSubscription)
	mux.HandleFunc("DELETE "+routes.MeSubscriptionByID, h.DeleteMeSubscription)
	mux.HandleFunc("GET "+routes.MeRatesChart, h.GetMeRatesChart)
	mux.HandleFunc("GET "+routes.MeRatesHistory, h.GetMeRatesHistory)
	mux.HandleFunc("POST "+routes.MeProfile, h.UpsertMeProfile)

	mux.HandleFunc("GET "+routes.Sources, h.ListSources)
	mux.HandleFunc("PATCH "+routes.SourceToggleActive, h.ToggleSourceActive)

	mux.HandleFunc("GET "+routes.SourceRates, h.ListRates)
	mux.HandleFunc("GET "+routes.SourceHistory, h.ListHistory)
	mux.HandleFunc("GET "+routes.SourceEventsFailed, h.ListSourceFailedEvents)
	// SourceSubscriptionsList must come before SourceSubscriptions to avoid prefix clash.
	mux.HandleFunc("GET "+routes.SourceSubscriptionsList, h.ListSourceSubscriptionDetails)
	mux.HandleFunc("GET "+routes.SourceSubscriptions, h.ListSourceSubscriptions)
	mux.HandleFunc("GET "+routes.SourceEventsDaily, h.ListSourceDailyEvents)

	mux.HandleFunc("GET "+routes.Stats, h.ListStats)
	mux.HandleFunc("GET "+routes.ErrorsExecution, h.ListExecutionErrors)
	mux.HandleFunc("GET "+routes.EventsPending, h.ListPendingEvents)

	// NotificationsFailed must be registered before Notifications so that
	// ServeMux longest-prefix matching selects the correct handler.
	mux.HandleFunc("GET "+routes.NotificationsFailed, h.ListFailedNotifications)
	mux.HandleFunc("GET "+routes.Notifications, h.ListNotifications)

	// MeWeatherCitiesSearch must be registered before MeWeatherCities so that
	// Go 1.22+ ServeMux longest-path matching selects the correct handler.
	mux.HandleFunc("GET "+routes.MeWeatherCurrent, h.GetMeWeatherCurrent)
	mux.HandleFunc("GET "+routes.MeWeatherCitiesSearch, h.SearchWeatherCities)
	mux.HandleFunc("GET "+routes.MeWeatherCities, h.ListMeWeatherCities)
	mux.HandleFunc("POST "+routes.MeWeatherCities, h.CreateMeWeatherCity)
	mux.HandleFunc("DELETE "+routes.MeWeatherCityByID, h.DeleteMeWeatherCity)

	// /ping is the liveness probe; /healthz is kept as a backward-compatible alias.
	mux.HandleFunc("GET "+routes.Ping, h.Ping)
	mux.HandleFunc("GET "+routes.Healthz, h.Ping)
	mux.HandleFunc("GET "+routes.HealthCheck, h.HealthCheck)

	return mux, nil
}

// healthCheckAgent is the contract for the dependency-health aggregator, threaded
// through the router to the HealthCheck handler. Nil is allowed; the handler
// returns 503 when no agent is wired.
type healthCheckAgent interface {
	CheckUp(ctx context.Context) (healthy bool, report map[string]string)
}

// meSubscriptionRepo threads the subscription repository through the router
// without depending on the concrete repository package.
type meSubscriptionRepo interface {
	ObtainRateUserSubscriptionsByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error)
	ObtainRateUserSubscriptionByID(ctx context.Context, id string) (*domain.RateUserSubscription, error)
	RetainRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error
	RemoveRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error
}

// meSourceRepo is a thin interface for source look-ups in the Mini App handler.
type meSourceRepo interface {
	ObtainRateSourceByName(ctx context.Context, name string) (*domain.RateSource, error)
	ObtainRateSourcesByNames(ctx context.Context, names []string) (map[string]domain.RateSource, error)
}

// meRateValueRepo is a thin interface for rate value look-ups in the Mini App handler.
type meRateValueRepo interface {
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, limit int64) ([]domain.RateValue, error)
	ObtainLatestRateValuesBySourceNames(ctx context.Context, names []string) (map[string]domain.RateValue, error)
}

// meProfileRepo is a thin interface for user-profile upserts (timezone).
type meProfileRepo interface {
	UpsertRateUserProfile(ctx context.Context, record *domain.RateUserProfile) error
}

// meWeatherCityRepo threads the weather city repository through the router layer.
type meWeatherCityRepo interface {
	RetainWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
	ObtainWeatherUserCitiesByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.WeatherUserCity, error)
	ObtainWeatherUserCityByID(ctx context.Context, id string) (*domain.WeatherUserCity, error)
	RemoveWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
}

// weatherGeocoder threads the geocoding provider through the router layer.
// The return type matches the handler's interface exactly ([]dto.WeatherCitySearchItem),
// so the adapter lives in cmd/web and not in this package.
type weatherGeocoder interface {
	Geocode(ctx context.Context, name string, count int) ([]dto.WeatherCitySearchItem, error)
}

// meWeatherObsRepo threads the weather observation repository through the router
// layer for the on-demand current-weather endpoint.
type meWeatherObsRepo interface {
	ObtainLatestObservation(ctx context.Context, locationID, provider string) (*domain.WeatherObservation, error)
}
