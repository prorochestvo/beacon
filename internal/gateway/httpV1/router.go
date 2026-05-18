// Package httpV1 wires the v1 HTTP handlers onto the provided ServeMux.
package httpV1

import (
	"context"
	"net/http"

	"github.com/seilbekskindirov/monitor/internal/application/rulegen"
	"github.com/seilbekskindirov/monitor/internal/application/service"
	"github.com/seilbekskindirov/monitor/internal/domain"
	v1 "github.com/seilbekskindirov/monitor/internal/gateway/httpV1/handlers"
	"github.com/seilbekskindirov/monitor/internal/gateway/httpV1/routes"
)

// meSubscriptionRepo is a thin interface used to thread the subscription repository
// through the router without depending on the concrete repository package.
type meSubscriptionRepo interface {
	ObtainRateUserSubscriptionsByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error)
}

// meSourceRepo is a thin interface for source look-ups in the Mini App handler.
type meSourceRepo interface {
	ObtainRateSourceByName(ctx context.Context, name string) (*domain.RateSource, error)
}

// meRateValueRepo is a thin interface for rate value look-ups in the Mini App handler.
type meRateValueRepo interface {
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, limit int64) ([]domain.RateValue, error)
}

// NewRouter registers all v1 HTTP routes on mux and returns it.
func NewRouter(
	mux *http.ServeMux,
	srvRateRestApi *service.RateRestApi,
	botToken string,
	subRepo meSubscriptionRepo,
	sourceRepo meSourceRepo,
	rateValueRepo meRateValueRepo,
	defaultGenerator v1.RulegenGenerator,
	generatorFactory v1.RulegenGeneratorFactory,
	adminChatID int64,
	lockMgr *rulegen.LockManager,
) (*http.ServeMux, error) {
	h, err := v1.NewHandler(srvRateRestApi, botToken, subRepo, sourceRepo, rateValueRepo,
		defaultGenerator, generatorFactory, adminChatID, lockMgr)
	if err != nil {
		return nil, err
	}

	// MeSubscriptions is registered before the /api/sources/... block.
	// /me and /sources are distinct prefixes, so no ambiguity.
	mux.HandleFunc("GET "+routes.MeSubscriptions, h.ListMeSubscriptions)

	mux.HandleFunc("GET "+routes.Sources, h.ListSources)
	mux.HandleFunc("PATCH "+routes.SourceToggleActive, h.ToggleSourceActive)

	// SourceRatesChart must come before SourceRates: the chart path is more specific
	// and Go's ServeMux selects the longest-matching literal prefix first.
	mux.HandleFunc("GET "+routes.SourceRatesChart, h.GetRatesChart)
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

	// SourceRulesGenerate is more specific than any existing GET on /api/sources/{name}/...
	// so Go's ServeMux longest-prefix rule routes it correctly without ordering constraints.
	mux.HandleFunc("POST "+routes.SourceRulesGenerate, h.GenerateRules)

	return mux, nil
}
