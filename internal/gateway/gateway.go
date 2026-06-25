// Package gateway is the composition root for the HTTP layer. It wires the
// service and repository dependencies into a ready-to-serve *http.ServeMux.
package gateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/seilbekskindirov/monitor/internal"
	appchart "github.com/seilbekskindirov/monitor/internal/application/chart"
	"github.com/seilbekskindirov/monitor/internal/application/service"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/gateway/httpV1"
)

// NewGateway builds the v1 HTTP mux with all routes registered, ready for
// http.ListenAndServe. chartSvc is required for GET /api/me/rates/chart.
func NewGateway(
	srvRateRestApi *service.RateRestApi,
	botToken string,
	subRepo meSubscriptionRepo,
	sourceRepo meSourceRepo,
	rateValueRepo meRateValueRepo,
	profileRepo meProfileRepo,
	chartSvc *appchart.Service,
) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	mux, err := httpV1.NewRouter(mux, srvRateRestApi, botToken, subRepo, sourceRepo, rateValueRepo, profileRepo, chartSvc)
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
