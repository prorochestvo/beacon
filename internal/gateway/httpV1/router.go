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
	"github.com/seilbekskindirov/beacon/internal/gateway/middleware"
)

// meCredentialMaxAge is how old a signed initData payload may be. Twenty-four hours
// matches the window Telegram itself treats a WebApp launch as current, and it is
// the value the handlers enforced individually before the check moved to one place.
const meCredentialMaxAge = 24 * time.Hour

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
	RemoveWeatherUserCitiesByLocation(ctx context.Context, userType domain.UserType, userID, locationID string) error
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
	h, err := v1.NewHandler(v1.Config{
		RateService:     srvRateRestApi,
		MeSubRepo:       subRepo,
		MeSourceRepo:    sourceRepo,
		MeRateValueRepo: rateValueRepo,
		MeProfileRepo:   profileRepo,
		WeatherCityRepo: weather.CityRepo,
		WeatherGeocoder: weather.Geocoder,
		WeatherObsRepo:  weather.ObsRepo,
		MeChartSvc:      chartSvc,
		HealthAgent:     healthAgent,
		ServerVersion:   serverVersion,
		ServerStart:     serverStart,
	})
	if err != nil {
		return nil, err
	}

	// Every authenticated route is registered on meMux, which is mounted once behind
	// the initData middleware below. A route's authentication therefore follows from
	// where it is registered rather than from remembering to check inside it — the
	// omission this arrangement exists to make impossible.
	meMux := http.NewServeMux()

	// MeSubscriptionsRaw must be registered before MeSubscriptions so Go 1.22+
	// ServeMux longest-path matching selects the more specific route.
	meMux.HandleFunc("GET "+routes.MeSubscriptionsRaw, h.ListMeSubscriptionsRaw)
	meMux.HandleFunc("GET "+routes.MeSubscriptions, h.ListMeSubscriptions)
	meMux.HandleFunc("POST "+routes.MeSubscriptions, h.CreateMeSubscription)
	meMux.HandleFunc("PATCH "+routes.MeSubscriptionByID, h.UpdateMeSubscription)
	meMux.HandleFunc("DELETE "+routes.MeSubscriptionByID, h.DeleteMeSubscription)
	meMux.HandleFunc("GET "+routes.MeRatesChart, h.GetMeRatesChart)
	meMux.HandleFunc("GET "+routes.MeRatesHistory, h.GetMeRatesHistory)
	meMux.HandleFunc("POST "+routes.MeProfile, h.UpsertMeProfile)

	// MeWeatherCitiesSearch must be registered before MeWeatherCities so that
	// Go 1.22+ ServeMux longest-path matching selects the correct handler.
	meMux.HandleFunc("GET "+routes.MeWeatherCurrent, h.GetMeWeatherCurrent)
	meMux.HandleFunc("GET "+routes.MeWeatherCitiesSearch, h.SearchWeatherCities)
	meMux.HandleFunc("GET "+routes.MeWeatherCities, h.ListMeWeatherCities)
	meMux.HandleFunc("POST "+routes.MeWeatherCities, h.CreateMeWeatherCity)
	meMux.HandleFunc("DELETE "+routes.MeWeatherCityByID, h.DeleteMeWeatherCity)
	meMux.HandleFunc("DELETE "+routes.MeWeatherLocationByID, h.DeleteMeWeatherLocation)

	// The pattern carries no method, so method matching happens inside meMux and a
	// wrong verb still answers 405 rather than 404.
	mux.Handle(routes.MePrefix, middleware.TelegramInitData(middleware.TelegramInitDataConfig{
		BotToken: botToken,
		MaxAge:   meCredentialMaxAge,
	})(meMux))

	mux.HandleFunc("GET "+routes.PublicRatesChart, h.GetPublicRatesChart)

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

	// /ping is the liveness probe; /healthz is kept as a backward-compatible alias.
	mux.HandleFunc("GET "+routes.Ping, h.Ping)
	mux.HandleFunc("GET "+routes.Healthz, h.Ping)
	mux.HandleFunc("GET "+routes.HealthCheck, h.HealthCheck)

	return mux, nil
}
