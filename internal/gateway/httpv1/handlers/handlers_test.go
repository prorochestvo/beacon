package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	appchart "github.com/seilbekskindirov/beacon/internal/application/chart"
	appprofile "github.com/seilbekskindirov/beacon/internal/application/profile"
	appsub "github.com/seilbekskindirov/beacon/internal/application/subscription"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/domain/ratepair"
	"github.com/seilbekskindirov/beacon/internal/dto"
	"github.com/seilbekskindirov/beacon/internal/gateway/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ meSubscriptionService = (*mockMeSubSvc)(nil)
var _ meProfileService = (*mockMeProfileSvc)(nil)
var _ rateService = (*mockRateService)(nil)
var _ meChartService = (*mockMeChartService)(nil)
var _ healthCheckAgent = (*mockHealthAgent)(nil)

// mockMeProfileSvc is a test double for meProfileService. It records what the
// handler derived from the request and can fail the write.
type mockMeProfileSvc struct {
	err error

	gotUserID  string
	gotProfile *appprofile.Profile
}

func (m *mockMeProfileSvc) UpsertMeProfile(_ context.Context, userID string, p appprofile.Profile) error {
	m.gotUserID, m.gotProfile = userID, &p
	return m.err
}

// mockHealthAgent is a test double for healthCheckAgent. healthy and report are
// returned verbatim from CheckUp so tests can exercise 200 vs 503 paths without
// a real inspector.
type mockHealthAgent struct {
	healthy bool
	report  map[string]string
}

func (m *mockHealthAgent) CheckUp(_ context.Context) (bool, map[string]string) {
	return m.healthy, m.report
}

func TestPing(t *testing.T) {
	t.Parallel()

	t.Run("always returns 200 with JSON status ok, touches no dependency", func(t *testing.T) {
		t.Parallel()
		h := newTestHandler(t, Config{})
		rr := httptest.NewRecorder()
		h.Ping(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ping", http.NoBody))
		require.Equal(t, http.StatusOK, rr.Code)
		require.JSONEq(t, `{"status":"ok"}`, rr.Body.String())
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	})
}

func TestHealthCheck(t *testing.T) {
	t.Parallel()

	t.Run("nil agent returns 503 with empty JSON body", func(t *testing.T) {
		t.Parallel()
		h := newTestHandler(t, Config{})
		rr := httptest.NewRecorder()
		h.HealthCheck(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/check", http.NoBody))
		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var body dto.HealthCheckResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.False(t, body.Status)
	})

	t.Run("all healthy returns 200 with status true and each component ok", func(t *testing.T) {
		t.Parallel()
		agent := &mockHealthAgent{healthy: true, report: map[string]string{"sqlite": "ok", "telegram": "ok"}}
		h := newTestHandler(t, Config{
			HealthAgent:   agent,
			ServerVersion: "v1.0.0",
			ServerStart:   time.Now(),
		})
		rr := httptest.NewRecorder()
		h.HealthCheck(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/check", http.NoBody))
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var body dto.HealthCheckResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.True(t, body.Status)
		require.Equal(t, "v1.0.0", body.Server.Version)
		require.NotEmpty(t, body.Server.Uptime)
		require.Equal(t, "ok", body.Services["sqlite"])
		require.Equal(t, "ok", body.Services["telegram"])
	})

	t.Run("unhealthy dependency returns 503 with status false and verbatim error message", func(t *testing.T) {
		t.Parallel()
		agent := &mockHealthAgent{healthy: false, report: map[string]string{"sqlite": "connection refused", "telegram": "ok"}}
		h := newTestHandler(t, Config{
			HealthAgent:   agent,
			ServerVersion: "v1.0.0",
			ServerStart:   time.Now(),
		})
		rr := httptest.NewRecorder()
		h.HealthCheck(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/check", http.NoBody))
		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
		var body dto.HealthCheckResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.False(t, body.Status)
		require.Equal(t, "connection refused", body.Services["sqlite"])
	})

	t.Run("zero serverStart produces empty uptime string", func(t *testing.T) {
		t.Parallel()
		agent := &mockHealthAgent{healthy: true, report: map[string]string{}}
		h := newTestHandler(t, Config{
			HealthAgent: agent,
		})
		rr := httptest.NewRecorder()
		h.HealthCheck(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/check", http.NoBody))
		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.HealthCheckResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Empty(t, body.Server.Uptime)
	})

	t.Run("advisory open-meteo failure yields HTTP 200 with component error", func(t *testing.T) {
		t.Parallel()
		// The aggregator returns healthy=true when only advisory inspectors fail;
		// the handler must map that to HTTP 200 even though a component has an error.
		agent := &mockHealthAgent{
			healthy: true,
			report:  map[string]string{"sqlite": "ok", "telegram": "ok", "open-meteo": "geocoding unreachable"},
		}
		h := newTestHandler(t, Config{
			HealthAgent:   agent,
			ServerVersion: "v1.0.0",
			ServerStart:   time.Now(),
		})
		rr := httptest.NewRecorder()
		h.HealthCheck(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/check", http.NoBody))
		require.Equal(t, http.StatusOK, rr.Code, "advisory failure must not flip HTTP status to 503")
		var body dto.HealthCheckResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.True(t, body.Status)
		require.Equal(t, "geocoding unreachable", body.Services["open-meteo"])
	})

	t.Run("critical sqlite failure yields HTTP 503", func(t *testing.T) {
		t.Parallel()
		// A critical dependency failure makes the aggregator return healthy=false;
		// the handler must map that to HTTP 503.
		agent := &mockHealthAgent{
			healthy: false,
			report:  map[string]string{"sqlite": "connection refused", "telegram": "ok", "open-meteo": "ok"},
		}
		h := newTestHandler(t, Config{
			HealthAgent:   agent,
			ServerVersion: "v1.0.0",
			ServerStart:   time.Now(),
		})
		rr := httptest.NewRecorder()
		h.HealthCheck(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/check", http.NoBody))
		require.Equal(t, http.StatusServiceUnavailable, rr.Code, "critical sqlite failure must return 503")
		var body dto.HealthCheckResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.False(t, body.Status)
		require.Equal(t, "connection refused", body.Services["sqlite"])
	})
}

func TestListSources(t *testing.T) {
	t.Parallel()

	t.Run("200 with sources", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{
			sources: []domain.RateSource{
				{Name: "src1", BaseCurrency: "USD", QuoteCurrency: "KZT", Interval: "1h"},
				{Name: "src2", BaseCurrency: "EUR", QuoteCurrency: "KZT", Interval: "2h"},
			},
			historyItems: []domain.ExecutionHistory{{
				ID:        "h1",
				Success:   true,
				Timestamp: time.Now().UTC(),
			}},
		}

		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []dto.SourceResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.Equal(t, "src1", body[0].Name)
	})

	t.Run("200 empty array when no sources", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{sources: nil}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)

		var body []dto.SourceResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Empty(t, body)
	})

	t.Run("500 on ObtainAllRateSources error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("200 with empty execution fields when bulk history fetch fails", func(t *testing.T) {
		t.Parallel()

		// Sources load succeeds; bulk history fetch fails. Handler must
		// degrade gracefully — return 200 with sources, execution fields empty.
		svc := &mockRateService{
			sources:        []domain.RateSource{{Name: "src1"}, {Name: "src2"}},
			historyBulkErr: errors.New("history unavailable"),
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		var body []dto.SourceResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
		require.Len(t, body, 2)
		for _, item := range body {
			require.Empty(t, item.LastRunAt, "graceful degradation: no execution metadata when bulk fetch failed")
		}
	})
}

func TestListRates(t *testing.T) {
	t.Parallel()

	t.Run("200 with rates", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{
			rates: []domain.RateValue{
				{ID: "r1", Price: 470.0, BaseCurrency: "USD", QuoteCurrency: "KZT", Timestamp: time.Now().UTC()},
				{ID: "r2", Price: 471.0, BaseCurrency: "USD", QuoteCurrency: "KZT", Timestamp: time.Now().UTC()},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/rates", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListRates(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []dto.RateResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
	})

	t.Run("200 empty", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{rates: nil}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/rates", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListRates(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body []dto.RateResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Empty(t, body)
	})

	t.Run("400 when name path param missing", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListRates(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources//rates", http.NoBody))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/rates", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListRates(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestListHistory(t *testing.T) {
	t.Parallel()

	t.Run("200 with records", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{
			historyItems: []domain.ExecutionHistory{
				{ID: "h1", Success: true, Timestamp: time.Now().UTC()},
				{ID: "h2", Success: false, Error: "oops", Timestamp: time.Now().UTC()},
				{ID: "h3", Success: true, Timestamp: time.Now().UTC()},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/history", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []dto.HistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 3)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/history", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListHistory(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestListNotifications(t *testing.T) {
	t.Parallel()

	t.Run("200 with notifications", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		svc := &mockRateService{
			events: []domain.RateUserEvent{
				{ID: "e1", UserType: domain.UserTypeTelegram, UserID: "111", Status: domain.RateUserEventStatusSent, CreatedAt: now},
				{ID: "e2", UserType: domain.UserTypeTelegram, UserID: "222", Status: domain.RateUserEventStatusFailed, CreatedAt: now},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListNotifications(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []dto.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.NotEmpty(t, body[0].ID)
		require.NotEmpty(t, body[0].UserType)
		require.NotEmpty(t, body[0].Status)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListNotifications(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestListFailedNotifications(t *testing.T) {
	t.Parallel()

	t.Run("200 with results using offset param", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		svc := &mockRateService{
			events: []domain.RateUserEvent{
				{ID: "e1", UserType: domain.UserTypeTelegram, UserID: "111", Status: domain.RateUserEventStatusFailed, CreatedAt: now},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListFailedNotifications(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?offset=50&limit=20", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []dto.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
	})

	t.Run("200 with no params returns default page", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{events: []domain.RateUserEvent{}}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListFailedNotifications(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListFailedNotifications(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestListPendingEvents(t *testing.T) {
	t.Parallel()

	t.Run("200 with pending events", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		svc := &mockRateService{
			events: []domain.RateUserEvent{
				{ID: "e1", UserType: domain.UserTypeTelegram, Status: domain.RateUserEventStatusPending, CreatedAt: now},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListPendingEvents(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/pending", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)

		var body []dto.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
		require.Empty(t, body[0].UserID, "user_id must be omitted")
	})

	t.Run("200 empty array when none pending", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{events: []domain.RateUserEvent{}}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListPendingEvents(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/pending", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListPendingEvents(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/pending", http.NoBody))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestListSourceFailedEvents(t *testing.T) {
	t.Parallel()

	t.Run("200 with failed events, user_id absent", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		svc := &mockRateService{
			events: []domain.RateUserEvent{
				{ID: "e1", UserType: domain.UserTypeTelegram, Status: domain.RateUserEventStatusFailed, LastError: "timeout", CreatedAt: now},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/events/failed?page=1", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceFailedEvents(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body []dto.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
		require.Empty(t, body[0].UserID, "user_id must not be present")
	})

	t.Run("400 when name missing", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListSourceFailedEvents(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources//events/failed", http.NoBody))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/events/failed", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceFailedEvents(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestListSourceSubscriptions(t *testing.T) {
	t.Parallel()

	t.Run("200 with subscription summaries", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{
			subscriptionSummaries: []domain.RateUserSubscriptionSummary{
				{
					SourceName:        "src1",
					UserType:          domain.UserTypeTelegram,
					SubscriptionCount: 3,
					SuccessCount:      10,
					FailedCount:       2,
				},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/subscriptions", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceSubscriptions(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body []dto.SubscriptionSummaryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
		require.Equal(t, "src1", body[0].SourceName)
		require.Empty(t, body[0].LastSentAt, "last_sent_at must be omitted when zero")
	})

	t.Run("400 when name missing", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListSourceSubscriptions(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources//subscriptions", http.NoBody))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/subscriptions", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceSubscriptions(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestHandler_ToggleSourceActive(t *testing.T) {
	t.Parallel()

	t.Run("204 on success", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/sources/src1/active", strings.NewReader(`{"active":true}`))
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code)
	})
	t.Run("404 when source not found", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{
			RateService: &mockRateService{err: internal.ErrNotFound},
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/sources/unknown/active", strings.NewReader(`{"active":true}`))
		req.SetPathValue("name", "unknown")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusNotFound, rr.Code)
	})
	t.Run("400 on malformed request body", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/sources/src1/active", strings.NewReader(`not-json`))
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("400 when name path param missing", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{})

		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/sources//active", strings.NewReader(`{"active":true}`)))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("500 on unexpected service error", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{
			RateService: &mockRateService{err: errors.New("db error")},
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/sources/src1/active", strings.NewReader(`{"active":true}`))
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestHandler_ListStats(t *testing.T) {
	t.Parallel()

	t.Run("200 with stats", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{stats: domain.StatsResult{SourcesTotal: 5, SourcesActive: 3, ErrorsTotal: 7}}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListStats(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/stats", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body dto.StatsResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, int64(5), body.SourcesTotal)
		require.Equal(t, int64(3), body.SourcesActive)
		require.Equal(t, int64(7), body.ErrorsTotal)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{
			RateService: &mockRateService{err: errors.New("db error")},
		})

		rr := httptest.NewRecorder()
		h.ListStats(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/stats", http.NoBody))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestHandler_ListSourceSubscriptionDetails(t *testing.T) {
	t.Parallel()

	t.Run("200 with subscription details", func(t *testing.T) {
		t.Parallel()

		notifiedAt := time.Now().UTC()
		svc := &mockRateService{
			subscriptionDetails: []domain.RateUserSubscriptionDetail{
				{ID: "sub1", SourceName: "src1", ConditionType: "percent", ConditionValue: "5", UserType: domain.UserTypeTelegram, LatestNotifiedAt: notifiedAt},
				{ID: "sub2", SourceName: "src1", ConditionType: "absolute", ConditionValue: "10", UserType: domain.UserTypeTelegram},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/subscriptions/list?page=1", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceSubscriptionDetails(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []dto.SubscriptionDetailResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.Equal(t, "sub1", body[0].ID)
		require.NotEmpty(t, body[0].LatestNotifiedAt, "latest_notified_at must be populated when non-zero")
		require.Empty(t, body[1].LatestNotifiedAt, "latest_notified_at must be omitted when zero")
	})
	t.Run("400 when name path param missing", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{})

		rr := httptest.NewRecorder()
		h.ListSourceSubscriptionDetails(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources//subscriptions/list", http.NoBody))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{
			RateService: &mockRateService{err: errors.New("db error")},
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/subscriptions/list", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceSubscriptionDetails(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestHandler_ListSourceDailyEvents(t *testing.T) {
	t.Parallel()

	t.Run("200 with daily event summaries", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{
			dailySummaries: []domain.RateUserEventDailySummary{
				{UserType: "telegram", Date: "2026-04-12", SuccessCount: 10, FailedCount: 1},
				{UserType: "telegram", Date: "2026-04-13", SuccessCount: 8, FailedCount: 0},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/events/daily?page=1", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceDailyEvents(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []dto.DailyEventResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.Equal(t, "2026-04-12", body[0].Date)
		require.Equal(t, int64(10), body[0].SuccessCount)
	})
	t.Run("400 when name path param missing", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{})

		rr := httptest.NewRecorder()
		h.ListSourceDailyEvents(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources//events/daily", http.NoBody))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{
			RateService: &mockRateService{err: errors.New("db error")},
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sources/src1/events/daily", http.NoBody)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceDailyEvents(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestHandler_ListExecutionErrors(t *testing.T) {
	t.Parallel()

	t.Run("200 with execution errors", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		svc := &mockRateService{
			historyItems: []domain.ExecutionHistory{
				{ID: "h1", SourceName: "src1", Success: false, Error: "timeout", Timestamp: now},
				{ID: "h2", SourceName: "src2", Success: false, Error: "parse error", Timestamp: now},
			},
		}
		h := newTestHandler(t, Config{
			RateService: svc,
		})

		rr := httptest.NewRecorder()
		h.ListExecutionErrors(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/errors/execution?page=1", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []dto.ExecutionErrorResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.Equal(t, "h1", body[0].ID)
		require.Equal(t, "src1", body[0].SourceName)
		require.Equal(t, "timeout", body[0].Error)
		require.NotEmpty(t, body[0].Timestamp)
	})
	t.Run("200 empty array on page with no records", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{
			RateService: &mockRateService{historyItems: nil},
		})

		rr := httptest.NewRecorder()
		h.ListExecutionErrors(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/errors/execution?page=99", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)

		var body []dto.ExecutionErrorResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Empty(t, body)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{
			RateService: &mockRateService{err: errors.New("db error")},
		})

		rr := httptest.NewRecorder()
		h.ListExecutionErrors(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/errors/execution", http.NoBody))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

type mockRateService struct {
	sources               []domain.RateSource
	rates                 []domain.RateValue
	historyItems          []domain.ExecutionHistory
	events                []domain.RateUserEvent
	subscriptionSummaries []domain.RateUserSubscriptionSummary
	subscriptionDetails   []domain.RateUserSubscriptionDetail
	dailySummaries        []domain.RateUserEventDailySummary
	stats                 domain.StatsResult
	err                   error
	// historyBulkErr lets a test fail the bulk execution-history call
	// independently of other methods — needed to exercise ListSources'
	// degradation path without making ObtainAllRateSources fail too.
	historyBulkErr error
}

func (m *mockRateService) ObtainAllRateSources(_ context.Context) ([]domain.RateSource, error) {
	return m.sources, m.err
}

func (m *mockRateService) UpdateRateSourceActive(_ context.Context, _ string, _ bool) error {
	return m.err
}

func (m *mockRateService) ObtainLastNRateValuesBySourceName(_ context.Context, _ string, _ int64) ([]domain.RateValue, error) {
	return m.rates, m.err
}

func (m *mockRateService) ObtainLastNExecutionHistoryBySourceName(_ context.Context, _ string, _ int64) ([]domain.ExecutionHistory, error) {
	return m.historyItems, m.err
}

func (m *mockRateService) ObtainLatestExecutionHistoryBySources(_ context.Context, names []string) (map[string]domain.ExecutionHistory, error) {
	if m.historyBulkErr != nil {
		return nil, m.historyBulkErr
	}
	if m.err != nil {
		return nil, m.err
	}
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	out := make(map[string]domain.ExecutionHistory, len(names))
	for _, h := range m.historyItems {
		if _, ok := want[h.SourceName]; ok {
			out[h.SourceName] = h
		}
	}
	return out, nil
}

func (m *mockRateService) ObtainLastSuccessNExecutionHistoryBySourceName(_ context.Context, _ string, _ int64) ([]domain.ExecutionHistory, error) {
	return m.historyItems, m.err
}

func (m *mockRateService) ObtainListOfLastRateUserEvent(_ context.Context, _ int64) ([]domain.RateUserEvent, error) {
	return m.events, m.err
}

func (m *mockRateService) ObtainFailedListOfRateUserEvent(_ context.Context, _, _ int64) ([]domain.RateUserEvent, error) {
	return m.events, m.err
}

func (m *mockRateService) ObtainPendingRateUserEvents(_ context.Context) ([]domain.RateUserEvent, error) {
	return m.events, m.err
}

func (m *mockRateService) ObtainFailedRateUserEventsBySourceName(_ context.Context, _ string, _, _ int64) ([]domain.RateUserEvent, error) {
	return m.events, m.err
}

func (m *mockRateService) ObtainSubscriptionSummaryBySource(_ context.Context, _ string) ([]domain.RateUserSubscriptionSummary, error) {
	return m.subscriptionSummaries, m.err
}

func (m *mockRateService) ObtainStats(_ context.Context) (domain.StatsResult, error) {
	return m.stats, m.err
}

func (m *mockRateService) ObtainRateUserSubscriptionsBySourcePaged(_ context.Context, _ string, _, _ int64) ([]domain.RateUserSubscriptionDetail, error) {
	return m.subscriptionDetails, m.err
}

func (m *mockRateService) ObtainDailyEventSummaryBySource(_ context.Context, _ string, _, _ int64) ([]domain.RateUserEventDailySummary, error) {
	return m.dailySummaries, m.err
}

func (m *mockRateService) ObtainLastNExecutionHistoryErrors(_ context.Context, _, _ int64) ([]domain.ExecutionHistory, error) {
	return m.historyItems, m.err
}

// mockMeSubSvc is a test double for meSubscriptionService. It records the
// arguments the handler derived from the request so the query-string parsing can
// be asserted, and replays whatever the test staged.
type mockMeSubSvc struct {
	rows  []appsub.SourceRow
	total int64
	err   error

	rawRows []appsub.ConditionRow
	rawErr  error

	createID  string
	createErr error
	updateErr error
	deleteErr error

	gotUserID   string
	gotQuery    string
	gotPage     int64
	gotPageSize int64

	gotID     string
	gotCreate *appsub.NewSubscription
	gotUpdate *appsub.ConditionUpdate
}

func (m *mockMeSubSvc) CreateMeSubscription(_ context.Context, userID string, req appsub.NewSubscription) (string, error) {
	m.gotUserID, m.gotCreate = userID, &req
	if m.createErr != nil {
		return "", m.createErr
	}
	if m.createID == "" {
		return "generated-id", nil
	}
	return m.createID, nil
}

func (m *mockMeSubSvc) UpdateMeSubscription(_ context.Context, userID, id string, upd appsub.ConditionUpdate) error {
	m.gotUserID, m.gotID, m.gotUpdate = userID, id, &upd
	return m.updateErr
}

func (m *mockMeSubSvc) DeleteMeSubscription(_ context.Context, userID, id string) error {
	m.gotUserID, m.gotID = userID, id
	return m.deleteErr
}

func (m *mockMeSubSvc) ObtainMeSubscriptions(_ context.Context, userID, query string, page, pageSize int64) ([]appsub.SourceRow, int64, error) {
	m.gotUserID, m.gotQuery, m.gotPage, m.gotPageSize = userID, query, page, pageSize
	if m.err != nil {
		return nil, 0, m.err
	}
	return m.rows, m.total, nil
}

func (m *mockMeSubSvc) ObtainMeSubscriptionsRaw(_ context.Context, userID string) ([]appsub.ConditionRow, error) {
	m.gotUserID = userID
	if m.rawErr != nil {
		return nil, m.rawErr
	}
	return m.rawRows, nil
}

// TestHandler_ListMeSubscriptions covers what stayed behind in the handler once
// the grouping, search and pagination rules moved to the application service:
// deriving the service arguments from the request, and rendering what comes back.
// The rules themselves are exercised in internal/application/subscription.
func TestHandler_ListMeSubscriptions(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(111)

	t.Run("derives the caller and the query arguments from the request", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/subscriptions?q=euro&page=2&page_size=10", http.NoBody)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, withCaller(req, callerUserID))

		require.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "111", svc.gotUserID, "the service is scoped to the authenticated caller, never to a request parameter")
		assert.Equal(t, "euro", svc.gotQuery)
		assert.Equal(t, int64(2), svc.gotPage)
		assert.Equal(t, int64(10), svc.gotPageSize)
	})

	t.Run("renders the rows and echoes the requested window", func(t *testing.T) {
		t.Parallel()

		collectedAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
		svc := &mockMeSubSvc{
			total: 12,
			rows: []appsub.SourceRow{{
				SourceName:    "src_a",
				SourceTitle:   "Source A",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Conditions:    []string{"delta:5"},
				LatestPrice:   470.5,
				LatestAt:      collectedAt,
			}},
		}

		h := newTestHandler(t, Config{MeSubSvc: svc})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/subscriptions?page=2&page_size=10", http.NoBody)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, withCaller(req, callerUserID))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeSubscriptionsResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))

		assert.Equal(t, int64(12), body.Total, "the total is the service's match count, not the page length")
		assert.Equal(t, int64(2), body.Page)
		assert.Equal(t, int64(10), body.PageSize)
		require.Len(t, body.Items, 1)
		assert.Equal(t, "src_a", body.Items[0].SourceName)
		assert.Equal(t, "Source A", body.Items[0].SourceTitle)
		assert.Equal(t, "USD", body.Items[0].BaseCurrency)
		assert.Equal(t, "KZT", body.Items[0].QuoteCurrency)
		assert.Equal(t, []string{"delta:5"}, body.Items[0].Conditions)
		assert.InDelta(t, 470.5, body.Items[0].LatestPrice, 0.001)
		assert.Equal(t, "2026-05-01T12:00:00Z", body.Items[0].LatestAt)
	})

	t.Run("an uncollected source omits latest_at rather than rendering year one", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{total: 1, rows: []appsub.SourceRow{{SourceName: "src_a", Conditions: []string{"delta:5"}}}}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/subscriptions", http.NoBody)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, withCaller(req, callerUserID))

		require.Equal(t, http.StatusOK, rr.Code)
		assert.NotContains(t, rr.Body.String(), "latest_at")
	})

	t.Run("empty result renders a non-nil items array", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{MeSubSvc: &mockMeSubSvc{}})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/subscriptions", http.NoBody)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, withCaller(req, callerUserID))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeSubscriptionsResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.NotNil(t, body.Items)
		assert.Empty(t, body.Items)
	})

	t.Run("400 on a non-integer page_size", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/subscriptions?page_size=many", http.NoBody)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, withCaller(req, callerUserID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Empty(t, svc.gotUserID, "a request rejected on parsing must not reach the service")
	})

	t.Run("500 on service failure", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{MeSubSvc: &mockMeSubSvc{err: errors.New("db down")}})
		h.logger = log.New(io.Discard, "", 0)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/subscriptions", http.NoBody)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, withCaller(req, callerUserID))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		assert.Contains(t, rr.Body.String(), "internal error")
	})
}

// TestHandler_UpsertMeProfile covers the handler's half: reading a bounded body
// and mapping the answer onto a status. Trimming, the required timezone and the
// locale cap are exercised in internal/application/profile.
func TestHandler_UpsertMeProfile(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(4242)

	post := func(t *testing.T, svc meProfileService, body string) (*httptest.ResponseRecorder, *Handler) {
		t.Helper()
		h := newTestHandler(t, Config{MeProfileSvc: svc, Logger: log.New(io.Discard, "", 0)})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/me/profile", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h.UpsertMeProfile(rr, withCaller(req, callerUserID))
		return rr, h
	}

	t.Run("204 on success, forwarding the caller and both fields", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeProfileSvc{}
		rr, _ := post(t, svc, `{"timezone":"Asia/Almaty","locale":"kk-KZ"}`)

		require.Equal(t, http.StatusNoContent, rr.Code)
		assert.Equal(t, strconv.FormatInt(callerUserID, 10), svc.gotUserID,
			"the profile is written for the authenticated caller, never for a value in the body")
		require.NotNil(t, svc.gotProfile)
		assert.Equal(t, "Asia/Almaty", svc.gotProfile.Timezone)
		assert.Equal(t, "kk-KZ", svc.gotProfile.Locale)
	})

	t.Run("400 on a malformed body, before the service is reached", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeProfileSvc{}
		rr, _ := post(t, svc, `not-json`)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid request body")
		assert.Nil(t, svc.gotProfile)
	})

	t.Run("400 on a body exceeding 1 KiB", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeProfileSvc{}
		rr, _ := post(t, svc, `{"timezone":"UTC","locale":"`+strings.Repeat("a", 2<<10)+`"}`)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Nil(t, svc.gotProfile)
	})

	t.Run("a PublicError from the service becomes a 400 carrying its message", func(t *testing.T) {
		t.Parallel()

		rr, _ := post(t, &mockMeProfileSvc{err: internal.NewPublicError("timezone is required")}, `{"timezone":""}`)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "timezone is required")
	})

	t.Run("500 on any other service failure", func(t *testing.T) {
		t.Parallel()

		rr, _ := post(t, &mockMeProfileSvc{err: errors.New("db down")}, `{"timezone":"UTC"}`)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
		assert.NotContains(t, rr.Body.String(), "db down")
	})
}

// mockMeChartService is a test double for meChartService.
type mockMeChartService struct {
	chart       *appchart.MeChart
	history     *appchart.MeHistoryResult
	publicChart *appchart.PublicChart
	publicTotal int64
	err         error
	// received captures the last arguments passed to ObtainMeHistory so
	// subtests can assert forwarding without a shared-state race. Each
	// subtest that needs to inspect these must use its own mock instance.
	received struct {
		sourceTitle string
	}
}

func (m *mockMeChartService) ObtainMeChartForPeriod(_ context.Context, _ string, _ int64) (*appchart.MeChart, error) {
	return m.chart, m.err
}

func (m *mockMeChartService) ObtainMeHistory(_ context.Context, _, _, sourceTitle string, _, _ int64) (*appchart.MeHistoryResult, error) {
	m.received.sourceTitle = sourceTitle
	return m.history, m.err
}

func (m *mockMeChartService) ObtainPublicChartForPeriod(_ context.Context, _, _, _ int64) (*appchart.PublicChart, int64, error) {
	return m.publicChart, m.publicTotal, m.err
}

func TestGetMeRatesChart(t *testing.T) {
	t.Parallel()

	t.Run("service error returns 500 with fallback message", func(t *testing.T) {
		t.Parallel()

		chartSvc := &mockMeChartService{err: errors.New("db exploded")}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 42))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})

	t.Run("returns 499 when context is cancelled mid-flight", func(t *testing.T) {
		t.Parallel()

		chartSvc := &mockMeChartService{err: context.Canceled}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 42))

		require.Equal(t, 499, rr.Code, "context.Canceled must produce 499, not 500")
		require.Contains(t, rr.Body.String(), "request cancelled")
	})

	t.Run("returns 499 when context deadline exceeded mid-flight", func(t *testing.T) {
		t.Parallel()

		chartSvc := &mockMeChartService{err: context.DeadlineExceeded}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 42))

		require.Equal(t, 499, rr.Code, "context.DeadlineExceeded must produce 499, not 500")
		require.Contains(t, rr.Body.String(), "request cancelled")
	})

	t.Run("valid call returns 200 with full DTO including two series and spread", func(t *testing.T) {
		t.Parallel()

		fixedTime := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
		spreadPct := 0.29
		chartSvc := &mockMeChartService{
			chart: &appchart.MeChart{
				Pairs: []appchart.PairRow{
					{
						Pair:      "USD/KZT",
						Category:  "fiat",
						SpreadPct: &spreadPct,
						Series: []appchart.SeriesRow{
							{
								Kind:     domain.RateSourceKindBID,
								Color:    ratepair.ColorBid,
								Latest:   487.55,
								DeltaPct: 3.6,
								Sparse:   false,
								Points: []appchart.SparkPoint{
									{Timestamp: fixedTime, Value: 480},
									{Timestamp: fixedTime.Add(time.Hour), Value: 487.55},
								},
							},
							{
								Kind:   domain.RateSourceKindASK,
								Color:  ratepair.ColorAsk,
								Latest: 488.95,
							},
						},
					},
				},
			},
		}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 123))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "7 days", body.Window)
		require.Len(t, body.Pairs, 1)

		row := body.Pairs[0]
		require.Equal(t, "USD/KZT", row.Pair, "pair label must be BID-natural BASE/QUOTE")
		require.Equal(t, "fiat", row.Category)
		require.NotNil(t, row.SpreadPct, "SpreadPct must be forwarded from service")
		require.InDelta(t, 0.29, *row.SpreadPct, 0.001)
		require.Len(t, row.Series, 2)

		bid := row.Series[0]
		require.Equal(t, "BID", bid.Kind)
		require.Equal(t, ratepair.ColorBid, bid.Color)
		require.InDelta(t, 3.6, bid.DeltaPct, 0.001)
		require.Len(t, bid.Points, 2)
		// Timestamp round-trip: JSON encodes time.Time as RFC3339 and decodes to UTC.
		require.Equal(t, fixedTime.UTC(), bid.Points[0].Timestamp.UTC())
		require.Equal(t, fixedTime.Add(time.Hour).UTC(), bid.Points[1].Timestamp.UTC())

		ask := row.Series[1]
		require.Equal(t, "ASK", ask.Kind)
		require.Equal(t, ratepair.ColorAsk, ask.Color)
	})

	t.Run("pair row groups BID and ASK into one row with label from service", func(t *testing.T) {
		t.Parallel()

		// The service sets pair.Pair; the handler is a thin marshaller — no flip logic.
		chartSvc := &mockMeChartService{
			chart: &appchart.MeChart{
				Pairs: []appchart.PairRow{
					{
						Pair:     "USD/KZT",
						Category: "fiat",
						Series: []appchart.SeriesRow{
							{Kind: domain.RateSourceKindASK, Color: ratepair.ColorAsk},
						},
					},
				},
			},
		}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 1))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		// The handler passes through whatever Pair label the service computed; no flip.
		require.Equal(t, "USD/KZT", body.Pairs[0].Pair)
		require.Nil(t, body.Pairs[0].SpreadPct, "SpreadPct must be nil when only one direction is present")
	})

	t.Run("503 when chart service is nil", func(t *testing.T) {
		t.Parallel()

		// nil meChartSvc must be caught after auth, before the service call, so
		// an unauthenticated caller cannot learn whether the service is wired.
		h := newTestHandler(t, Config{})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 99))

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
		require.Contains(t, rr.Body.String(), "chart service unavailable")
	})

	t.Run("no period param defaults to 7 days window", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{chart: &appchart.MeChart{Pairs: []appchart.PairRow{}}}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 1))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "7 days", body.Window)
	})

	t.Run("explicit period=30 yields Window 30 days", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{chart: &appchart.MeChart{Pairs: []appchart.PairRow{}}}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart?period=30", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 1))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "30 days", body.Window)
	})

	t.Run("invalid integer period returns 400", func(t *testing.T) {
		t.Parallel()
		h := newTestHandler(t, Config{
			MeChartSvc: &mockMeChartService{},
		})
		for _, bad := range []string{"45", "-1", "0", "361"} {

			t.Run(bad, func(t *testing.T) {
				t.Parallel()
				req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart?period="+bad, http.NoBody)
				req.Header.Set("X-Telegram-Init-Data", "valid")
				rr := httptest.NewRecorder()
				h.GetMeRatesChart(rr, withCaller(req, 1))
				require.Equal(t, http.StatusBadRequest, rr.Code)
				require.Contains(t, rr.Body.String(), "period must be one of 7, 30, 90, 180, 360")
			})
		}
	})

	t.Run("non-integer period returns 400", func(t *testing.T) {
		t.Parallel()
		h := newTestHandler(t, Config{
			MeChartSvc: &mockMeChartService{},
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart?period=7d", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 1))
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "period must be one of 7, 30, 90, 180, 360")
	})

	t.Run("empty period value defaults to 7", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{chart: &appchart.MeChart{Pairs: []appchart.PairRow{}}}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart?period=", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 1))
		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "7 days", body.Window)
	})

	t.Run("effective_days round-trips through GetMeRatesChart", func(t *testing.T) {
		t.Parallel()
		// Service returns a series with EffectiveDays=7 (capped from a longer period).
		// The handler must forward it to the DTO without modification.
		// Window must still be "360 days" (the requested period), not the effective one.
		chartSvc := &mockMeChartService{
			chart: &appchart.MeChart{
				Pairs: []appchart.PairRow{
					{
						Pair:     "USD/KZT",
						Category: "fiat",
						Series: []appchart.SeriesRow{
							{
								Kind:          domain.RateSourceKindBID,
								Color:         ratepair.ColorBid,
								Latest:        490.0,
								DeltaPct:      2.1,
								Sparse:        false,
								EffectiveDays: 7,
								Points:        []appchart.SparkPoint{{Timestamp: time.Now(), Value: 490.0}},
							},
						},
					},
				},
			},
		}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/chart?period=360", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, withCaller(req, 1))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "360 days", body.Window, "Window must reflect the requested period, not effective coverage")
		require.Len(t, body.Pairs, 1)
		require.Len(t, body.Pairs[0].Series, 1)
		assert.Equal(t, 7, body.Pairs[0].Series[0].EffectiveDays,
			"EffectiveDays from the service must round-trip to the DTO unchanged")
	})
}

func TestGetPublicRatesChart(t *testing.T) {
	t.Parallel()

	t.Run("no period param defaults to 7 days window", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}, publicTotal: 0}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "7 days", body.Window)
	})

	t.Run("explicit period=90 yields Window 90 days", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart?period=90", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "90 days", body.Window)
	})

	t.Run("invalid period returns 400", func(t *testing.T) {
		t.Parallel()
		h := newTestHandler(t, Config{
			MeChartSvc: &mockMeChartService{},
		})

		for _, bad := range []string{"45", "7d", "-1"} {

			t.Run(bad, func(t *testing.T) {
				t.Parallel()
				rr := httptest.NewRecorder()
				h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart?period="+bad, http.NoBody))
				require.Equal(t, http.StatusBadRequest, rr.Code)
				require.Contains(t, rr.Body.String(), "period must be one of 7, 30, 90, 180, 360")
			})
		}
	})

	t.Run("empty period defaults to 7", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart?period=", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "7 days", body.Window)
	})

	t.Run("503 when chart service is nil", func(t *testing.T) {
		t.Parallel()
		h := newTestHandler(t, Config{})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart", http.NoBody))

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("500 on service error", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{err: errors.New("db dead")}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart", http.NoBody))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("499 on context cancelled", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{err: context.Canceled}
		h := newTestHandler(t, Config{
			MeChartSvc: chartSvc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart", http.NoBody))

		require.Equal(t, 499, rr.Code)
		assert.Contains(t, rr.Body.String(), "request cancelled")
	})

	t.Run("happy path returns paginated rows", func(t *testing.T) {
		t.Parallel()
		pc := &appchart.PublicChart{
			Pairs: []appchart.PairRow{
				{Pair: "USD/KZT", Series: []appchart.SeriesRow{{Kind: "BID", Color: "#1D9E75"}}},
				{Pair: "EUR/KZT", Series: []appchart.SeriesRow{{Kind: "BID", Color: "#1D9E75"}}},
			},
		}
		svc := &mockMeChartService{publicChart: pc, publicTotal: 2}
		h := newTestHandler(t, Config{
			MeChartSvc: svc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var resp dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Equal(t, "7 days", resp.Window)
		assert.Equal(t, 1, resp.Page)
		assert.Equal(t, 20, resp.Limit)
		assert.EqualValues(t, 2, resp.Total)
		assert.Len(t, resp.Pairs, 2)
	})

	t.Run("page greater than 1 is forwarded", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{
			publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{{Pair: "USD/KZT"}}},
			publicTotal: 25,
		}
		h := newTestHandler(t, Config{
			MeChartSvc: svc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart?page=2", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Equal(t, 2, resp.Page)
	})

	t.Run("limit cap clamps to 100", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}}
		h := newTestHandler(t, Config{
			MeChartSvc: svc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart?limit=999", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Equal(t, 100, resp.Limit)
	})

	t.Run("non-integer limit returns 400", func(t *testing.T) {
		t.Parallel()
		h := newTestHandler(t, Config{
			MeChartSvc: &mockMeChartService{},
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart?limit=abc", http.NoBody))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		publicErr := internal.NewPublicError("limit must be a number")
		assert.Contains(t, rr.Body.String(), publicErr.Details())
	})

	t.Run("page overflow is clamped", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}}
		h := newTestHandler(t, Config{
			MeChartSvc: svc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart?page=9223372036854775807", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.EqualValues(t, int64(1)<<30, resp.Page)
	})

	t.Run("service returns plain error returns 500", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{err: errors.New("db dead")}
		h := newTestHandler(t, Config{
			MeChartSvc: svc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart", http.NoBody))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}` + "\n"
		assert.JSONEq(t, errFallbackMessage, rr.Body.String())
	})

	t.Run("effective_days round-trips through GetPublicRatesChart", func(t *testing.T) {
		t.Parallel()
		// Service returns a series with EffectiveDays=7 (capped from a longer period).
		// The handler must forward it to the DTO without modification.
		// Window must still equal the requested period, not the effective coverage.
		svc := &mockMeChartService{
			publicChart: &appchart.PublicChart{
				Pairs: []appchart.PairRow{
					{
						Pair:     "USD/KZT",
						Category: "fiat",
						Series: []appchart.SeriesRow{
							{
								Kind:          domain.RateSourceKindBID,
								Color:         ratepair.ColorBid,
								Latest:        490.0,
								Sparse:        false,
								EffectiveDays: 7,
								Points:        []appchart.SparkPoint{{Timestamp: time.Now(), Value: 490.0}},
							},
						},
					},
				},
			},
			publicTotal: 1,
		}
		h := newTestHandler(t, Config{
			MeChartSvc: svc,
		})

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/public/rates/chart?period=90", http.NoBody))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "90 days", body.Window, "Window must reflect the requested period, not effective coverage")
		require.Len(t, body.Pairs, 1)
		require.Len(t, body.Pairs[0].Series, 1)
		assert.Equal(t, 7, body.Pairs[0].Series[0].EffectiveDays,
			"EffectiveDays from the service must round-trip to the DTO unchanged")
	})
}

func TestHandler_GetMeRatesHistory(t *testing.T) {
	t.Parallel()

	newH := func(t *testing.T, svc meChartService) *Handler {
		t.Helper()
		h := newTestHandler(t, Config{
			MeChartSvc: svc,
		})
		return h
	}

	emptyHistoryResult := &appchart.MeHistoryResult{
		Pair:  "USD/KZT",
		Total: 0,
		Items: []appchart.MeHistoryRowResult{},
	}

	t.Run("200 OK with valid initData and pair", func(t *testing.T) {
		t.Parallel()
		bidVal := 490.0
		svc := &mockMeChartService{history: &appchart.MeHistoryResult{
			Pair:  "USD/KZT",
			Total: 1,
			Items: []appchart.MeHistoryRowResult{
				{SourceTitle: "Test", Timestamp: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC), Bid: &bidVal},
			},
		}}
		h := newH(t, svc)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Equal(t, "USD/KZT", resp.Pair)
		assert.EqualValues(t, 1, resp.Total)
		require.Len(t, resp.Items, 1)
		require.NotNil(t, resp.Items[0].Bid)
		assert.InDelta(t, 490.0, *resp.Items[0].Bid, 0.001)
	})

	t.Run("200 OK with empty items when no subscription matches", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Empty(t, resp.Items)
		assert.EqualValues(t, 0, resp.Total)
	})

	t.Run("400 when pair missing", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "pair is required")
	})

	t.Run("400 when pair is whitespace only", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=+++", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "pair is required")
	})

	t.Run("400 when limit not a number", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT&limit=abc", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "limit must be a number")
	})

	t.Run("499 when ctx canceled", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{err: context.Canceled})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, 499, rr.Code)
		assert.Contains(t, rr.Body.String(), "request cancelled")
	})

	t.Run("499 when ctx deadline exceeded", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{err: context.DeadlineExceeded})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, 499, rr.Code)
		assert.Contains(t, rr.Body.String(), "request cancelled")
	})

	t.Run("limit clamped to max", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT&limit=9999", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.EqualValues(t, meHistoryMaxLimit, resp.Limit)
	})

	t.Run("limit defaults when absent", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.EqualValues(t, meHistoryDefaultLimit, resp.Limit)
	})

	t.Run("page defaults to 1 when absent or invalid", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT&page=bad", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Equal(t, 1, resp.Page)
	})

	t.Run("X-Telegram-Init-Data is not echoed in any log line", func(t *testing.T) {
		t.Parallel()
		secretInitData := "secret-init-data-payload-must-not-leak"

		// Inject a per-test logger to capture log output without touching the
		// global log.SetOutput (which would race with concurrent parallel subtests
		// that also call internalError).
		var logBuf strings.Builder
		testLogger := log.New(&logBuf, "", 0)

		svc := &mockMeChartService{err: errors.New("deliberate service error to exercise the log path")}
		h := newTestHandler(t, Config{
			MeChartSvc: svc,
		})
		h.logger = testLogger

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", secretInitData)
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		assert.NotContains(t, logBuf.String(), secretInitData, "handler must not log the X-Telegram-Init-Data value")
	})

	t.Run("forwards source_title to service", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{history: emptyHistoryResult}
		h := newH(t, svc)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT&source_title=Kaspi", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "Kaspi", svc.received.sourceTitle)
	})

	t.Run("omits source_title when query param absent", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{history: emptyHistoryResult}
		h := newH(t, svc)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		assert.Empty(t, svc.received.sourceTitle)
	})

	t.Run("trims whitespace around source_title", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{history: emptyHistoryResult}
		h := newH(t, svc)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT&source_title=+Kaspi+", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "Kaspi", svc.received.sourceTitle)
	})

	t.Run("equity row Last and LastDeltaPct reach JSON and Bid Ask are nil", func(t *testing.T) {
		t.Parallel()
		v := 221.50
		d := 1.25
		svc := &mockMeChartService{history: &appchart.MeHistoryResult{
			Pair:  "AAPL/USD",
			Total: 1,
			Items: []appchart.MeHistoryRowResult{
				{SourceTitle: "Yahoo Finance", Timestamp: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC), Last: &v, LastDeltaPct: &d},
			},
		}}
		h := newH(t, svc)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=AAPL/USD", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		bodyBytes := rr.Body.Bytes()
		var resp dto.MeHistoryResponse
		require.NoError(t, json.Unmarshal(bodyBytes, &resp))
		require.Len(t, resp.Items, 1)
		require.NotNil(t, resp.Items[0].Last, "Last must be set for equity row")
		assert.InDelta(t, 221.50, *resp.Items[0].Last, 0.001)
		require.NotNil(t, resp.Items[0].LastDeltaPct, "LastDeltaPct must be set for equity row")
		assert.InDelta(t, 1.25, *resp.Items[0].LastDeltaPct, 0.001)
		assert.Nil(t, resp.Items[0].Bid, "Bid must be nil for equity row")
		assert.Nil(t, resp.Items[0].Ask, "Ask must be nil for equity row")
	})

	t.Run("FX BID row JSON omits last and last_delta_pct keys", func(t *testing.T) {
		t.Parallel()
		bid := 490.0
		delta := 0.5
		svc := &mockMeChartService{history: &appchart.MeHistoryResult{
			Pair:  "USD/KZT",
			Total: 1,
			Items: []appchart.MeHistoryRowResult{
				{SourceTitle: "Test", Timestamp: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC), Bid: &bid, BidDeltaPct: &delta},
			},
		}}
		h := newH(t, svc)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/rates/history?pair=USD/KZT", http.NoBody)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, withCaller(req, 42))

		require.Equal(t, http.StatusOK, rr.Code)
		bodyBytes := rr.Body.Bytes()
		// Decode into raw map to assert key absence (omitempty).
		var raw struct {
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.Unmarshal(bodyBytes, &raw))
		require.Len(t, raw.Items, 1)
		_, hasLast := raw.Items[0]["last"]
		_, hasLastDelta := raw.Items[0]["last_delta_pct"]
		assert.False(t, hasLast, "last key must be absent for FX BID row (omitempty)")
		assert.False(t, hasLastDelta, "last_delta_pct key must be absent for FX BID row (omitempty)")
	})
}

// TestHandler_ListMeSubscriptionsRaw covers the rendering half of the editor
// endpoint. Ordering and source enrichment belong to the application service and
// are exercised in internal/application/subscription.
func TestHandler_ListMeSubscriptionsRaw(t *testing.T) {
	t.Parallel()

	const callerID = int64(555)

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	t.Run("200 empty items when the caller has no subscriptions", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/subscriptions/raw", http.NoBody)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptionsRaw(rr, withCaller(req, callerID))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeSubscriptionsRawResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.NotNil(t, body.Items)
		assert.Empty(t, body.Items)
		assert.Equal(t, "555", svc.gotUserID)
	})

	t.Run("200 renders per-condition rows in the order the service returned them", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{rawRows: []appsub.ConditionRow{
			{
				ID: "id-1", SourceName: "src_a", SourceTitle: "Alpha",
				BaseCurrency: "USD", QuoteCurrency: "KZT",
				ConditionType: "delta", ConditionValue: "0.5", UpdatedAt: now,
			},
			{
				ID: "id-2", SourceName: "src_b", SourceTitle: "Beta",
				BaseCurrency: "EUR", QuoteCurrency: "KZT",
				ConditionType: "interval", ConditionValue: "1h", UpdatedAt: now.Add(-time.Hour),
			},
		}}

		h := newTestHandler(t, Config{MeSubSvc: svc})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/subscriptions/raw", http.NoBody)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptionsRaw(rr, withCaller(req, callerID))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeSubscriptionsRawResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body.Items, 2)

		assert.Equal(t, "id-1", body.Items[0].ID)
		assert.Equal(t, "src_a", body.Items[0].SourceName)
		assert.Equal(t, "Alpha", body.Items[0].SourceTitle)
		assert.Equal(t, "USD", body.Items[0].BaseCurrency)
		assert.Equal(t, "KZT", body.Items[0].QuoteCurrency)
		assert.Equal(t, "delta", body.Items[0].ConditionType)
		assert.Equal(t, "0.5", body.Items[0].ConditionValue)
		assert.Equal(t, "2026-05-01T12:00:00Z", body.Items[0].UpdatedAt)
		assert.Equal(t, "id-2", body.Items[1].ID, "the service owns the ordering; rendering must not reshuffle it")
	})

	t.Run("500 on service failure", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{MeSubSvc: &mockMeSubSvc{rawErr: errors.New("db down")}})
		h.logger = log.New(io.Discard, "", 0)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/subscriptions/raw", http.NoBody)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptionsRaw(rr, withCaller(req, callerID))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})
}

// TestHandler_CreateMeSubscription covers what the handler still owns: reading
// the body, handing the caller and the request to the application service, and
// turning what comes back into a status. The rules — an unknown source, an
// unparseable condition — are exercised in internal/application/subscription.
func TestHandler_CreateMeSubscription(t *testing.T) {
	t.Parallel()

	const callerID = int64(42)

	newReq := func(body string) *http.Request {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/me/subscriptions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	t.Run("201 with the generated id, built from the caller and the body", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{createID: "sub-new"}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, withCaller(newReq(`{"source_name":"src_a","condition_type":"delta","condition_value":"5"}`), callerID))

		require.Equal(t, http.StatusCreated, rr.Code)
		var resp dto.MeSubscriptionCreateResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Equal(t, "sub-new", resp.ID)

		assert.Equal(t, "42", svc.gotUserID, "ownership comes from the authenticated caller, never from the body")
		require.NotNil(t, svc.gotCreate)
		assert.Equal(t, "src_a", svc.gotCreate.SourceName)
		assert.Equal(t, domain.ConditionTypeDelta, svc.gotCreate.ConditionType)
		assert.Equal(t, "5", svc.gotCreate.ConditionValue)
	})

	t.Run("400 on a malformed body, before the service is reached", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, withCaller(newReq(`not-json`), callerID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid request body")
		assert.Nil(t, svc.gotCreate)
	})

	t.Run("400 on a body exceeding 4 KiB", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, withCaller(newReq(strings.Repeat("x", 5<<10)), callerID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Nil(t, svc.gotCreate)
	})

	t.Run("a PublicError from the service becomes a 400 carrying its message", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{createErr: internal.NewPublicError("unknown source")}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, withCaller(newReq(`{"source_name":"no_such","condition_type":"delta","condition_value":"5"}`), callerID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "unknown source")
	})

	t.Run("500 on any other service failure, with the detail kept out of the body", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{MeSubSvc: &mockMeSubSvc{createErr: errors.New("db down")}})
		h.logger = log.New(io.Discard, "", 0)
		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, withCaller(newReq(`{"source_name":"src_a","condition_type":"delta","condition_value":"5"}`), callerID))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
		assert.NotContains(t, rr.Body.String(), "db down")
	})
}

func TestHandler_UpdateMeSubscription(t *testing.T) {
	t.Parallel()

	const callerID = int64(10)

	newReq := func(id, body string) *http.Request {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/me/subscriptions/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", id)
		return req
	}

	t.Run("204 on success, forwarding the caller, the id and the condition", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, withCaller(newReq("sub-001", `{"condition_type":"interval","condition_value":"1h"}`), callerID))

		require.Equal(t, http.StatusNoContent, rr.Code)
		assert.Equal(t, "10", svc.gotUserID)
		assert.Equal(t, "sub-001", svc.gotID)
		require.NotNil(t, svc.gotUpdate)
		assert.Equal(t, domain.ConditionTypeInterval, svc.gotUpdate.ConditionType)
		assert.Equal(t, "1h", svc.gotUpdate.ConditionValue)
	})

	t.Run("404 on a missing subscription and on another user's", func(t *testing.T) {
		t.Parallel()

		// One sentinel covers both, so there is one response and no way to tell
		// a row that does not exist from one that belongs to somebody else.
		svc := &mockMeSubSvc{updateErr: internal.ErrNotFound}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, withCaller(newReq("sub-other", `{"condition_type":"delta","condition_value":"1"}`), callerID))

		require.Equal(t, http.StatusNotFound, rr.Code,
			"another user's subscription is 404, never 403 — a 403 confirms it exists")
		require.Contains(t, rr.Body.String(), "subscription not found")
	})

	t.Run("400 on a missing id, before the service is reached", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, withCaller(newReq("", `{"condition_type":"delta","condition_value":"1"}`), callerID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "missing subscription id")
		assert.Empty(t, svc.gotID)
	})

	t.Run("400 on a malformed body", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, withCaller(newReq("sub-001", `not-json`), callerID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid request body")
		assert.Empty(t, svc.gotID)
	})

	t.Run("400 on a body exceeding 4 KiB", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, withCaller(newReq("sub-001", strings.Repeat("x", 5<<10)), callerID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid request body")
		assert.Empty(t, svc.gotID, "the cap has to bite before the ownership query, not after it")
	})

	t.Run("a PublicError from the service becomes a 400 carrying its message", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{updateErr: internal.NewPublicError("invalid condition value for delta: check the format and try again")}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, withCaller(newReq("sub-001", `{"condition_type":"delta","condition_value":"x"}`), callerID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid condition")
	})

	t.Run("500 on any other service failure", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{MeSubSvc: &mockMeSubSvc{updateErr: errors.New("db down")}})
		h.logger = log.New(io.Discard, "", 0)
		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, withCaller(newReq("sub-001", `{"condition_type":"delta","condition_value":"1"}`), callerID))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})
}

func TestHandler_DeleteMeSubscription(t *testing.T) {
	t.Parallel()

	const callerID = int64(10)

	newReq := func(id string) *http.Request {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/me/subscriptions/"+id, http.NoBody)
		req.SetPathValue("id", id)
		return req
	}

	t.Run("204 on success, forwarding the caller and the id", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, withCaller(newReq("sub-001"), callerID))

		require.Equal(t, http.StatusNoContent, rr.Code)
		assert.Equal(t, "10", svc.gotUserID)
		assert.Equal(t, "sub-001", svc.gotID)
	})

	t.Run("404 on a missing subscription and on another user's", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{MeSubSvc: &mockMeSubSvc{deleteErr: internal.ErrNotFound}})
		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, withCaller(newReq("sub-other"), callerID))

		require.Equal(t, http.StatusNotFound, rr.Code,
			"another user's subscription is 404, never 403 — a 403 confirms it exists")
		require.Contains(t, rr.Body.String(), "subscription not found")
	})

	t.Run("400 on a missing id, before the service is reached", func(t *testing.T) {
		t.Parallel()

		svc := &mockMeSubSvc{}
		h := newTestHandler(t, Config{MeSubSvc: svc})
		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, withCaller(newReq(""), callerID))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "missing subscription id")
		assert.Empty(t, svc.gotID)
	})

	t.Run("500 on any other service failure", func(t *testing.T) {
		t.Parallel()

		h := newTestHandler(t, Config{MeSubSvc: &mockMeSubSvc{deleteErr: errors.New("db down")}})
		h.logger = log.New(io.Discard, "", 0)
		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, withCaller(newReq("sub-001"), callerID))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})
}

// TestNewHandler_Config covers what the weather wiring guards used to: before
// this refactor every weather handler opened with a nil check answering 503, so
// a composition root that forgot a dependency produced a service that started
// happily and failed one request at a time. The check now happens once, at
// construction.
func TestNewHandler_Config(t *testing.T) {
	t.Parallel()

	complete := func() Config {
		return Config{
			RateService:     &mockRateService{},
			MeSubSvc:        &mockMeSubSvc{},
			MeProfileSvc:    &mockMeProfileSvc{},
			MeWeatherSvc:    &mockMeWeatherSvc{},
			WeatherGeocoder: &mockWeatherGeocoder{},
		}
	}

	t.Run("a complete config yields a handler that needs nothing attached", func(t *testing.T) {
		t.Parallel()
		h, err := NewHandler(complete())
		require.NoError(t, err)
		require.NotNil(t, h)
	})

	t.Run("each required dependency is rejected by name", func(t *testing.T) {
		t.Parallel()
		without := map[string]func(*Config){
			"RateService":     func(c *Config) { c.RateService = nil },
			"MeSubSvc":        func(c *Config) { c.MeSubSvc = nil },
			"MeProfileSvc":    func(c *Config) { c.MeProfileSvc = nil },
			"MeWeatherSvc":    func(c *Config) { c.MeWeatherSvc = nil },
			"WeatherGeocoder": func(c *Config) { c.WeatherGeocoder = nil },
		}
		for name, drop := range without {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				cfg := complete()
				drop(&cfg)
				h, err := NewHandler(cfg)
				require.Error(t, err, "a config without %s must be rejected", name)
				require.Nil(t, h)
				require.Contains(t, err.Error(), name)
			})
		}
	})

	t.Run("every absentee is named at once", func(t *testing.T) {
		t.Parallel()
		_, err := NewHandler(Config{})
		require.Error(t, err)
		for _, name := range []string{
			"RateService", "MeSubSvc", "MeProfileSvc",
			"MeWeatherSvc", "WeatherGeocoder",
		} {
			require.Contains(t, err.Error(), name)
		}
	})

	t.Run("the optional dependencies may stay nil", func(t *testing.T) {
		t.Parallel()
		cfg := complete()
		cfg.MeChartSvc = nil
		cfg.HealthAgent = nil
		h, err := NewHandler(cfg)
		require.NoError(t, err, "MeChartSvc and HealthAgent answer 503 when absent; that is a deployment choice, not a wiring bug")
		require.Nil(t, h.meChartSvc)
		require.Nil(t, h.healthAgent)
	})

	t.Run("a nil logger falls back to the standard logger", func(t *testing.T) {
		t.Parallel()
		h, err := NewHandler(complete())
		require.NoError(t, err)
		require.Same(t, log.Default(), h.logger)

		own := log.New(io.Discard, "", 0)
		cfg := complete()
		cfg.Logger = own
		h, err = NewHandler(cfg)
		require.NoError(t, err)
		require.Same(t, own, h.logger)
	})
}

// newTestHandler builds a Handler with every required dependency defaulted to an
// empty mock, so a test names only what it actually exercises.
//
// The optional dependencies — MeChartSvc and HealthAgent — are passed through
// untouched on purpose: several tests assert the 503 their absence produces, so
// a helper that supplied them would quietly delete that coverage.
func newTestHandler(t *testing.T, cfg Config) *Handler {
	t.Helper()

	if cfg.RateService == nil {
		cfg.RateService = &mockRateService{}
	}
	if cfg.MeSubSvc == nil {
		cfg.MeSubSvc = &mockMeSubSvc{}
	}
	if cfg.MeProfileSvc == nil {
		cfg.MeProfileSvc = &mockMeProfileSvc{}
	}
	if cfg.MeWeatherSvc == nil {
		cfg.MeWeatherSvc = &mockMeWeatherSvc{}
	}
	if cfg.WeatherGeocoder == nil {
		cfg.WeatherGeocoder = &mockWeatherGeocoder{}
	}

	h, err := NewHandler(cfg)
	require.NoError(t, err)
	return h
}

// TestHandlersFailClosedWithoutACaller covers the safety net under the middleware.
// Authentication happens once, at the mount; this is what happens if a route ever
// ends up outside it. Serving would be an authentication bypass, so every handler
// that reads the caller must refuse instead.
func TestHandlersFailClosedWithoutACaller(t *testing.T) {
	t.Parallel()

	authenticated := map[string]func(*Handler) http.HandlerFunc{
		"GET /api/v1/me/subscriptions":            func(h *Handler) http.HandlerFunc { return h.ListMeSubscriptions },
		"GET /api/v1/me/subscriptions/raw":        func(h *Handler) http.HandlerFunc { return h.ListMeSubscriptionsRaw },
		"POST /api/v1/me/subscriptions":           func(h *Handler) http.HandlerFunc { return h.CreateMeSubscription },
		"PATCH /api/v1/me/subscriptions/{id}":     func(h *Handler) http.HandlerFunc { return h.UpdateMeSubscription },
		"DELETE /api/v1/me/subscriptions/{id}":    func(h *Handler) http.HandlerFunc { return h.DeleteMeSubscription },
		"GET /api/v1/me/rates/chart":              func(h *Handler) http.HandlerFunc { return h.GetMeRatesChart },
		"GET /api/v1/me/rates/history":            func(h *Handler) http.HandlerFunc { return h.GetMeRatesHistory },
		"POST /api/v1/me/profile":                 func(h *Handler) http.HandlerFunc { return h.UpsertMeProfile },
		"GET /api/v1/me/weather/current":          func(h *Handler) http.HandlerFunc { return h.GetMeWeatherCurrent },
		"GET /api/v1/me/weather/cities/search":    func(h *Handler) http.HandlerFunc { return h.SearchWeatherCities },
		"GET /api/v1/me/weather/cities":           func(h *Handler) http.HandlerFunc { return h.ListMeWeatherCities },
		"POST /api/v1/me/weather/cities":          func(h *Handler) http.HandlerFunc { return h.CreateMeWeatherCity },
		"DELETE /api/v1/me/weather/cities/{id}":   func(h *Handler) http.HandlerFunc { return h.DeleteMeWeatherCity },
		"DELETE /api/v1/me/weather/locations/{i}": func(h *Handler) http.HandlerFunc { return h.DeleteMeWeatherLocation },
	}

	for name, pick := range authenticated {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newTestHandler(t, Config{MeChartSvc: &mockMeChartService{}})
			rr := httptest.NewRecorder()
			// Deliberately not wrapped in withCaller: this is a request the middleware
			// never saw.
			pick(h)(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/me/anything", http.NoBody))

			require.Equal(t, http.StatusUnauthorized, rr.Code,
				"%s served a request carrying no authenticated caller", name)
		})
	}
}

// withCaller returns r carrying the caller id the initData middleware would have put
// on its context. Handlers no longer authenticate — they read what the middleware
// established — so a test that invokes one directly has to supply it. A request
// without it is what an unmounted route would produce, and the handlers answer 401.
func withCaller(r *http.Request, userID int64) *http.Request {
	return r.WithContext(middleware.WithUserID(r.Context(), userID))
}
