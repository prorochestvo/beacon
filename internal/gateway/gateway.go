// Package gateway is the composition root for the HTTP layer. It wires the
// service and repository dependencies into a ready-to-serve *http.ServeMux.
package gateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/application/rulegen"
	"github.com/seilbekskindirov/monitor/internal/application/service"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/gateway/httpV1"
	v1handlers "github.com/seilbekskindirov/monitor/internal/gateway/httpV1/handlers"
)

// meSubscriptionRepo is a pass-through interface from the concrete repository layer.
type meSubscriptionRepo interface {
	ObtainRateUserSubscriptionsByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error)
}

// meSourceRepo is a pass-through interface for source look-ups.
type meSourceRepo interface {
	ObtainRateSourceByName(ctx context.Context, name string) (*domain.RateSource, error)
}

// meRateValueRepo is a pass-through interface for rate value look-ups.
type meRateValueRepo interface {
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, limit int64) ([]domain.RateValue, error)
}

// NewGateway builds the v1 HTTP mux with all routes registered.
// It returns the configured *http.ServeMux ready to be passed to http.ListenAndServe.
func NewGateway(
	srvRateRestApi *service.RateRestApi,
	botToken string,
	subRepo meSubscriptionRepo,
	sourceRepo meSourceRepo,
	rateValueRepo meRateValueRepo,
	defaultGenerator v1handlers.RulegenGenerator,
	generatorFactory v1handlers.RulegenGeneratorFactory,
	adminChatID int64,
	lockMgr *rulegen.LockManager,
) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	mux, err := httpV1.NewRouter(mux, srvRateRestApi, botToken, subRepo, sourceRepo, rateValueRepo,
		defaultGenerator, generatorFactory, adminChatID, lockMgr)
	if err != nil {
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}
	return mux, nil
}
