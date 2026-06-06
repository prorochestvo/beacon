package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	appchart "github.com/seilbekskindirov/monitor/internal/application/chart"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/domain/ratepair"
	"github.com/seilbekskindirov/monitor/internal/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ meSubscriptionRepository = (*mockMeSubRepo)(nil)
var _ meSourceRepository = (*mockMeSourceRepo)(nil)
var _ meRateValueRepository = (*mockMeRateValueRepo)(nil)
var _ meProfileRepository = (*mockMeProfileRepo)(nil)
var _ rateService = (*mockRateService)(nil)
var _ meChartService = (*mockMeChartService)(nil)

// mockMeProfileRepo captures the last RateUserProfile upsert and lets a test
// inject an error from UpsertRateUserProfile to exercise the failure path.
type mockMeProfileRepo struct {
	upsertErr  error
	upsertCall *domain.RateUserProfile
}

func (m *mockMeProfileRepo) UpsertRateUserProfile(_ context.Context, record *domain.RateUserProfile) error {
	m.upsertCall = record
	return m.upsertErr
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	t.Run("200 when service CheckUP succeeds", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.Healthz(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.JSONEq(t, `{"status":"ok"}`, rr.Body.String())
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	})

	t.Run("503 when service CheckUP fails", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db unreachable")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.Healthz(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
		require.JSONEq(t, `{"status":"unavailable"}`, rr.Body.String())
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
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

		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequest(http.MethodGet, "/", nil))

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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusOK, rr.Code)

		var body []dto.SourceResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Empty(t, body)
	})

	t.Run("500 on ObtainAllRateSources error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequest(http.MethodGet, "/", nil))

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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequest(http.MethodGet, "/", nil))

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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/rates", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/rates", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListRates(rr, httptest.NewRequest(http.MethodGet, "/api/sources//rates", nil))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/rates", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/history", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/history", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListNotifications(rr, httptest.NewRequest(http.MethodGet, "/", nil))

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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListNotifications(rr, httptest.NewRequest(http.MethodGet, "/", nil))

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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListFailedNotifications(rr, httptest.NewRequest(http.MethodGet, "/?offset=50&limit=20", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []dto.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
	})

	t.Run("200 with no params returns default page", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{events: []domain.RateUserEvent{}}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListFailedNotifications(rr, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListFailedNotifications(rr, httptest.NewRequest(http.MethodGet, "/", nil))

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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListPendingEvents(rr, httptest.NewRequest(http.MethodGet, "/api/events/pending", nil))

		require.Equal(t, http.StatusOK, rr.Code)

		var body []dto.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
		require.Empty(t, body[0].UserID, "user_id must be omitted")
	})

	t.Run("200 empty array when none pending", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{events: []domain.RateUserEvent{}}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListPendingEvents(rr, httptest.NewRequest(http.MethodGet, "/api/events/pending", nil))

		require.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListPendingEvents(rr, httptest.NewRequest(http.MethodGet, "/api/events/pending", nil))

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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/events/failed?page=1", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSourceFailedEvents(rr, httptest.NewRequest(http.MethodGet, "/api/sources//events/failed", nil))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/events/failed", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/subscriptions", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSourceSubscriptions(rr, httptest.NewRequest(http.MethodGet, "/api/sources//subscriptions", nil))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/subscriptions", nil)
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

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPatch, "/api/sources/src1/active", strings.NewReader(`{"active":true}`))
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code)
	})
	t.Run("404 when source not found", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: internal.ErrNotFound}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPatch, "/api/sources/unknown/active", strings.NewReader(`{"active":true}`))
		req.SetPathValue("name", "unknown")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusNotFound, rr.Code)
	})
	t.Run("400 on malformed request body", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPatch, "/api/sources/src1/active", strings.NewReader(`not-json`))
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("400 when name path param missing", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, httptest.NewRequest(http.MethodPatch, "/api/sources//active", strings.NewReader(`{"active":true}`)))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("500 on unexpected service error", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPatch, "/api/sources/src1/active", strings.NewReader(`{"active":true}`))
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListStats(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))

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

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListStats(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))

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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/subscriptions/list?page=1", nil)
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

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSourceSubscriptionDetails(rr, httptest.NewRequest(http.MethodGet, "/api/sources//subscriptions/list", nil))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/subscriptions/list", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/events/daily?page=1", nil)
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

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSourceDailyEvents(rr, httptest.NewRequest(http.MethodGet, "/api/sources//events/daily", nil))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/events/daily", nil)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListExecutionErrors(rr, httptest.NewRequest(http.MethodGet, "/api/errors/execution?page=1", nil))

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

		h, err := NewHandler(&mockRateService{historyItems: nil}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListExecutionErrors(rr, httptest.NewRequest(http.MethodGet, "/api/errors/execution?page=99", nil))

		require.Equal(t, http.StatusOK, rr.Code)

		var body []dto.ExecutionErrorResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Empty(t, body)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListExecutionErrors(rr, httptest.NewRequest(http.MethodGet, "/api/errors/execution", nil))

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

func (m *mockRateService) CheckUP(_ context.Context) error {
	return m.err
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

// mockMeSubRepo is a test double for meSubscriptionRepository.
type mockMeSubRepo struct {
	subs      map[string][]domain.RateUserSubscription
	byID      map[string]*domain.RateUserSubscription
	retained  []*domain.RateUserSubscription
	removed   []*domain.RateUserSubscription
	err       error
	retainErr error
	removeErr error
}

func (m *mockMeSubRepo) ObtainRateUserSubscriptionsByUserID(_ context.Context, _ domain.UserType, userID string) ([]domain.RateUserSubscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.subs[userID], nil
}

func (m *mockMeSubRepo) ObtainRateUserSubscriptionByID(_ context.Context, id string) (*domain.RateUserSubscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.byID == nil {
		return nil, nil
	}
	return m.byID[id], nil
}

func (m *mockMeSubRepo) RetainRateUserSubscription(_ context.Context, record *domain.RateUserSubscription) error {
	if m.retainErr != nil {
		return m.retainErr
	}
	if record.ID == "" {
		record.ID = "generated-id"
	}
	m.retained = append(m.retained, record)
	return nil
}

func (m *mockMeSubRepo) RemoveRateUserSubscription(_ context.Context, record *domain.RateUserSubscription) error {
	if m.removeErr != nil {
		return m.removeErr
	}
	m.removed = append(m.removed, record)
	return nil
}

// mockMeSourceRepo is a test double for meSourceRepository.
type mockMeSourceRepo struct {
	sources map[string]*domain.RateSource
	err     error
}

func (m *mockMeSourceRepo) ObtainRateSourceByName(_ context.Context, name string) (*domain.RateSource, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.sources == nil {
		return nil, nil
	}
	return m.sources[name], nil
}

func (m *mockMeSourceRepo) ObtainRateSourcesByNames(_ context.Context, names []string) (map[string]domain.RateSource, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make(map[string]domain.RateSource, len(names))
	for _, n := range names {
		if s, ok := m.sources[n]; ok && s != nil {
			out[n] = *s
		}
	}
	return out, nil
}

// mockMeRateValueRepo is a test double for meRateValueRepository.
type mockMeRateValueRepo struct {
	rates map[string][]domain.RateValue
	err   error
}

func (m *mockMeRateValueRepo) ObtainLatestRateValuesBySourceNames(_ context.Context, names []string) (map[string]domain.RateValue, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make(map[string]domain.RateValue, len(names))
	for _, n := range names {
		if rates, ok := m.rates[n]; ok && len(rates) > 0 {
			out[n] = rates[0]
		}
	}
	return out, nil
}

func (m *mockMeRateValueRepo) ObtainLastNRateValuesBySourceName(_ context.Context, name string, _ int64) ([]domain.RateValue, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rates[name], nil
}

// alwaysValidateInitData is a fake validator that always succeeds and returns the given userID.
func alwaysValidateInitData(userID int64) func(string, string, time.Duration, time.Time) (int64, error) {
	return func(_, _ string, _ time.Duration, _ time.Time) (int64, error) {
		return userID, nil
	}
}

// alwaysRejectInitData is a fake validator that always fails.
func alwaysRejectInitData(initData, _ string, _ time.Duration, _ time.Time) (int64, error) {
	return 0, errors.New("invalid")
}

func TestHandler_ListMeSubscriptions(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(111)
	callerIDStr := "111"
	otherIDStr := "222"

	callerSub := domain.RateUserSubscription{
		ID: "sub1", UserType: domain.UserTypeTelegram, UserID: callerIDStr,
		SourceName: "src_a", ConditionType: "delta", ConditionValue: "5",
	}
	otherSub := domain.RateUserSubscription{
		ID: "sub2", UserType: domain.UserTypeTelegram, UserID: otherIDStr,
		SourceName: "src_b", ConditionType: "interval", ConditionValue: "1h",
	}
	srcA := &domain.RateSource{Name: "src_a", Title: "Source A", BaseCurrency: "USD", QuoteCurrency: "KZT"}

	t.Run("rejects missing initData with 401", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil))

		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Contains(t, rr.Body.String(), "unauthorized")
	})

	t.Run("rejects bad hash with 401", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil)
		req.Header.Set("X-Telegram-Init-Data", "hash=badvalue&auth_date=1234")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("?initData= query string is not read (header-only auth)", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		// Capture the initData the handler hands to the validator; with no
		// header set, the handler must pass an empty string (NOT the URL value)
		// so the HMAC token never leaks into access logs or Referer headers.
		var seen string
		h.validateInitData = func(initData, _ string, _ time.Duration, _ time.Time) (int64, error) {
			seen = initData
			if initData == "" {
				return 0, errors.New("missing initData")
			}
			return callerUserID, nil
		}

		req := httptest.NewRequest(http.MethodGet,
			"/api/me/subscriptions?initData=should_be_ignored", nil)
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, req)

		require.Equal(t, "", seen, "handler must not source initData from the URL query")
		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Contains(t, rr.Body.String(), "unauthorized")
	})

	t.Run("happy path returns only caller's subscriptions", func(t *testing.T) {
		t.Parallel()

		subRepo := &mockMeSubRepo{
			subs: map[string][]domain.RateUserSubscription{
				callerIDStr: {callerSub},
				otherIDStr:  {otherSub},
			},
		}
		sourceRepo := &mockMeSourceRepo{
			sources: map[string]*domain.RateSource{"src_a": srcA},
		}
		rateRepo := &mockMeRateValueRepo{
			rates: map[string][]domain.RateValue{
				"src_a": {{Price: 470.5, Timestamp: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}},
			},
		}

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, rateRepo, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body dto.MeSubscriptionsResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, int64(1), body.Total)
		require.Len(t, body.Items, 1)
		assert.Equal(t, "src_a", body.Items[0].SourceName)
		assert.Equal(t, "Source A", body.Items[0].SourceTitle)
		assert.InDelta(t, 470.5, body.Items[0].LatestPrice, 0.001)
		assert.NotEmpty(t, body.Items[0].LatestAt)
	})

	t.Run("search filters by source title", func(t *testing.T) {
		t.Parallel()

		subRepo := &mockMeSubRepo{
			subs: map[string][]domain.RateUserSubscription{
				callerIDStr: {
					{SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"},
					{SourceName: "src_b", ConditionType: "interval", ConditionValue: "1h"},
				},
			},
		}
		sourceRepo := &mockMeSourceRepo{
			sources: map[string]*domain.RateSource{
				"src_a": {Name: "src_a", Title: "Euro Bank", BaseCurrency: "EUR", QuoteCurrency: "KZT"},
				"src_b": {Name: "src_b", Title: "Dollar Bank", BaseCurrency: "USD", QuoteCurrency: "KZT"},
			},
		}
		rateRepo := &mockMeRateValueRepo{}

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, rateRepo, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions?q=euro", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body dto.MeSubscriptionsResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, int64(1), body.Total)
		require.Len(t, body.Items, 1)
		assert.Equal(t, "src_a", body.Items[0].SourceName)
	})

	t.Run("paginates correctly for 12 subscriptions on page 2", func(t *testing.T) {
		t.Parallel()

		subs := make([]domain.RateUserSubscription, 12)
		sources := make(map[string]*domain.RateSource, 12)
		for i := range 12 {
			name := "src_" + strconv.Itoa(i)
			subs[i] = domain.RateUserSubscription{
				SourceName:    name,
				ConditionType: "delta", ConditionValue: "1",
			}
			sources[name] = &domain.RateSource{Name: name, Title: "Source " + strconv.Itoa(i), BaseCurrency: "USD", QuoteCurrency: "KZT"}
		}
		subRepo := &mockMeSubRepo{subs: map[string][]domain.RateUserSubscription{callerIDStr: subs}}
		sourceRepo := &mockMeSourceRepo{sources: sources}
		rateRepo := &mockMeRateValueRepo{}

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, rateRepo, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions?page=2&page_size=10", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body dto.MeSubscriptionsResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, int64(12), body.Total)
		require.Len(t, body.Items, 2, "page 2 of 10-per-page with 12 items should return 2")
		assert.Equal(t, int64(2), body.Page)
		assert.Equal(t, int64(10), body.PageSize)
	})
}

func TestHandler_UpsertMeProfile(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(424242)

	t.Run("401 when initData missing", func(t *testing.T) {
		t.Parallel()
		profileRepo := &mockMeProfileRepo{}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, profileRepo, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodPost, "/api/me/profile",
			strings.NewReader(`{"timezone":"Asia/Almaty"}`))
		rr := httptest.NewRecorder()
		h.UpsertMeProfile(rr, req)
		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Nil(t, profileRepo.upsertCall, "upsert must not be called when auth fails")
	})

	t.Run("400 on empty timezone", func(t *testing.T) {
		t.Parallel()
		profileRepo := &mockMeProfileRepo{}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, profileRepo, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/profile",
			strings.NewReader(`{"timezone":""}`))
		req.Header.Set("X-Telegram-Init-Data", "fake-but-allowed")
		rr := httptest.NewRecorder()
		h.UpsertMeProfile(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Nil(t, profileRepo.upsertCall)
	})

	t.Run("400 on malformed JSON", func(t *testing.T) {
		t.Parallel()
		profileRepo := &mockMeProfileRepo{}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, profileRepo, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/profile",
			strings.NewReader(`{not json`))
		req.Header.Set("X-Telegram-Init-Data", "fake")
		rr := httptest.NewRecorder()
		h.UpsertMeProfile(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("400 surfaces PublicError from repo", func(t *testing.T) {
		t.Parallel()
		profileRepo := &mockMeProfileRepo{upsertErr: internal.NewPublicError("Invalid timezone.")}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, profileRepo, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/profile",
			strings.NewReader(`{"timezone":"Atlantis/Atlantis"}`))
		req.Header.Set("X-Telegram-Init-Data", "fake")
		rr := httptest.NewRecorder()
		h.UpsertMeProfile(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "Invalid timezone.")
	})

	t.Run("500 on infrastructure error from repo", func(t *testing.T) {
		t.Parallel()
		profileRepo := &mockMeProfileRepo{upsertErr: errors.New("db dead")}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, profileRepo, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/profile",
			strings.NewReader(`{"timezone":"UTC"}`))
		req.Header.Set("X-Telegram-Init-Data", "fake")
		rr := httptest.NewRecorder()
		h.UpsertMeProfile(rr, req)
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("204 on success and persisted record carries the right identity", func(t *testing.T) {
		t.Parallel()
		profileRepo := &mockMeProfileRepo{}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, profileRepo, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/profile",
			strings.NewReader(`{"timezone":"Asia/Almaty","locale":"kk-KZ"}`))
		req.Header.Set("X-Telegram-Init-Data", "fake")
		rr := httptest.NewRecorder()
		h.UpsertMeProfile(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code)
		require.NotNil(t, profileRepo.upsertCall)
		require.Equal(t, domain.UserTypeTelegram, profileRepo.upsertCall.UserType)
		require.Equal(t, strconv.FormatInt(callerUserID, 10), profileRepo.upsertCall.UserID)
		require.Equal(t, "Asia/Almaty", profileRepo.upsertCall.Timezone)
		require.Equal(t, "kk-KZ", profileRepo.upsertCall.Locale)
	})

	t.Run("204 when locale is omitted — timezone alone is sufficient", func(t *testing.T) {
		t.Parallel()
		profileRepo := &mockMeProfileRepo{}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, profileRepo, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodPost, "/api/me/profile",
			strings.NewReader(`{"timezone":"UTC"}`))
		req.Header.Set("X-Telegram-Init-Data", "fake")
		rr := httptest.NewRecorder()
		h.UpsertMeProfile(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code)
		require.Equal(t, "", profileRepo.upsertCall.Locale)
	})

	t.Run("400 when locale exceeds 64 chars", func(t *testing.T) {
		t.Parallel()
		profileRepo := &mockMeProfileRepo{}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, profileRepo, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		body := `{"timezone":"UTC","locale":"` + strings.Repeat("a", 65) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/me/profile", strings.NewReader(body))
		req.Header.Set("X-Telegram-Init-Data", "fake")
		rr := httptest.NewRecorder()
		h.UpsertMeProfile(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Nil(t, profileRepo.upsertCall, "upsert must not run when length check fails")
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

	t.Run("missing header returns 401", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, &mockMeChartService{})
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil))

		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Contains(t, rr.Body.String(), "unauthorized")
	})

	t.Run("invalid HMAC returns 401", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, &mockMeChartService{})
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil)
		req.Header.Set("X-Telegram-Init-Data", "hash=bad")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("service error returns 500 with fallback message", func(t *testing.T) {
		t.Parallel()

		chartSvc := &mockMeChartService{err: errors.New("db exploded")}
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(42)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})

	t.Run("returns 499 when context is cancelled mid-flight", func(t *testing.T) {
		t.Parallel()

		chartSvc := &mockMeChartService{err: context.Canceled}
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(42)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

		require.Equal(t, 499, rr.Code, "context.Canceled must produce 499, not 500")
		require.Contains(t, rr.Body.String(), "request cancelled")
	})

	t.Run("returns 499 when context deadline exceeded mid-flight", func(t *testing.T) {
		t.Parallel()

		chartSvc := &mockMeChartService{err: context.DeadlineExceeded}
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(42)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

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
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(123)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

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
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(1)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		// The handler passes through whatever Pair label the service computed; no flip.
		require.Equal(t, "USD/KZT", body.Pairs[0].Pair)
		require.Nil(t, body.Pairs[0].SpreadPct, "SpreadPct must be nil when only one direction is present")
	})

	t.Run("init data is read from header only, not query string", func(t *testing.T) {
		t.Parallel()

		var capturedInitData string
		chartSvc := &mockMeChartService{chart: &appchart.MeChart{}}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)
		h.validateInitData = func(initData, _ string, _ time.Duration, _ time.Time) (int64, error) {
			capturedInitData = initData
			return 0, errors.New("reject")
		}

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart?initData=should-not-read", nil)
		// Header is empty — the handler must not fall back to the query param.
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Equal(t, "", capturedInitData, "handler must pass the empty header value, not the query param")
	})

	t.Run("503 when chart service is nil", func(t *testing.T) {
		t.Parallel()

		// nil meChartSvc must be caught after auth, before the service call, so
		// an unauthenticated caller cannot learn whether the service is wired.
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(99)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
		require.Contains(t, rr.Body.String(), "chart service unavailable")
	})

	t.Run("expired payload returns 401", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, &mockMeChartService{})
		require.NoError(t, err)
		h.validateInitData = func(_, _ string, _ time.Duration, _ time.Time) (int64, error) {
			return 0, internal.NewPublicError("init data is too old")
		}

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil)
		req.Header.Set("X-Telegram-Init-Data", "stale-but-valid-hmac")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("no period param defaults to 7 days window", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{chart: &appchart.MeChart{Pairs: []appchart.PairRow{}}}
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(1)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "7 days", body.Window)
	})

	t.Run("explicit period=30 yields Window 30 days", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{chart: &appchart.MeChart{Pairs: []appchart.PairRow{}}}
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(1)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart?period=30", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "30 days", body.Window)
	})

	t.Run("invalid integer period returns 400", func(t *testing.T) {
		t.Parallel()
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, &mockMeChartService{})
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(1)

		for _, bad := range []string{"45", "-1", "0", "361"} {
			bad := bad
			t.Run(bad, func(t *testing.T) {
				t.Parallel()
				req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart?period="+bad, nil)
				req.Header.Set("X-Telegram-Init-Data", "valid")
				rr := httptest.NewRecorder()
				h.GetMeRatesChart(rr, req)
				require.Equal(t, http.StatusBadRequest, rr.Code)
				require.Contains(t, rr.Body.String(), "period must be one of 7, 30, 90, 180, 360")
			})
		}
	})

	t.Run("non-integer period returns 400", func(t *testing.T) {
		t.Parallel()
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, &mockMeChartService{})
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(1)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart?period=7d", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "period must be one of 7, 30, 90, 180, 360")
	})

	t.Run("empty period value defaults to 7", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{chart: &appchart.MeChart{Pairs: []appchart.PairRow{}}}
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(1)

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/chart?period=", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesChart(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "7 days", body.Window)
	})
}

func TestGetPublicRatesChart(t *testing.T) {
	t.Parallel()

	t.Run("no period param defaults to 7 days window", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}, publicTotal: 0}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "7 days", body.Window)
	})

	t.Run("explicit period=90 yields Window 90 days", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart?period=90", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "90 days", body.Window)
	})

	t.Run("invalid period returns 400", func(t *testing.T) {
		t.Parallel()
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, &mockMeChartService{})
		require.NoError(t, err)

		for _, bad := range []string{"45", "7d", "-1"} {
			bad := bad
			t.Run(bad, func(t *testing.T) {
				t.Parallel()
				rr := httptest.NewRecorder()
				h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart?period="+bad, nil))
				require.Equal(t, http.StatusBadRequest, rr.Code)
				require.Contains(t, rr.Body.String(), "period must be one of 7, 30, 90, 180, 360")
			})
		}
	})

	t.Run("empty period defaults to 7", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart?period=", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, "7 days", body.Window)
	})

	t.Run("503 when chart service is nil", func(t *testing.T) {
		t.Parallel()
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart", nil))

		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("500 on service error", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{err: errors.New("db dead")}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart", nil))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("499 on context cancelled", func(t *testing.T) {
		t.Parallel()
		chartSvc := &mockMeChartService{err: context.Canceled}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, chartSvc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart", nil))

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
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, svc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var resp dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Equal(t, "7 days", resp.Window)
		assert.EqualValues(t, 1, resp.Page)
		assert.EqualValues(t, 20, resp.Limit)
		assert.EqualValues(t, 2, resp.Total)
		assert.Len(t, resp.Pairs, 2)
	})

	t.Run("page greater than 1 is forwarded", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{
			publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{{Pair: "USD/KZT"}}},
			publicTotal: 25,
		}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, svc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart?page=2", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.EqualValues(t, 2, resp.Page)
	})

	t.Run("limit cap clamps to 100", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, svc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart?limit=999", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.EqualValues(t, 100, resp.Limit)
	})

	t.Run("non-integer limit returns 400", func(t *testing.T) {
		t.Parallel()
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, &mockMeChartService{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart?limit=abc", nil))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		publicErr := internal.NewPublicError("limit must be a number")
		assert.Contains(t, rr.Body.String(), publicErr.Details())
	})

	t.Run("page overflow is clamped", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{publicChart: &appchart.PublicChart{Pairs: []appchart.PairRow{}}}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, svc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart?page=9223372036854775807", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.PublicChartResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.EqualValues(t, int64(1)<<30, resp.Page)
	})

	t.Run("service returns plain error returns 500", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{err: errors.New("db dead")}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, svc)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetPublicRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/public/rates/chart", nil))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		const errFallbackMessage = `{"error":"internal error"}` + "\n"
		assert.Equal(t, errFallbackMessage, rr.Body.String())
	})
}

func TestHandler_GetMeRatesHistory(t *testing.T) {
	t.Parallel()

	newH := func(t *testing.T, svc meChartService) *Handler {
		t.Helper()
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, svc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(42)
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
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Equal(t, "USD/KZT", resp.Pair)
		assert.EqualValues(t, 1, resp.Total)
		require.Len(t, resp.Items, 1)
		require.NotNil(t, resp.Items[0].Bid)
		assert.Equal(t, 490.0, *resp.Items[0].Bid)
	})

	t.Run("200 OK with empty items when no subscription matches", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.Empty(t, resp.Items)
		assert.EqualValues(t, 0, resp.Total)
	})

	t.Run("400 when pair missing", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "pair is required")
	})

	t.Run("400 when pair is whitespace only", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=+++", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "pair is required")
	})

	t.Run("400 when limit not a number", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT&limit=abc", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "limit must be a number")
	})

	t.Run("401 when initData invalid", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{})
		h.validateInitData = alwaysRejectInitData
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT", nil)
		req.Header.Set("X-Telegram-Init-Data", "bad")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("499 when ctx canceled", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{err: context.Canceled})
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, 499, rr.Code)
		assert.Contains(t, rr.Body.String(), "request cancelled")
	})

	t.Run("499 when ctx deadline exceeded", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{err: context.DeadlineExceeded})
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, 499, rr.Code)
		assert.Contains(t, rr.Body.String(), "request cancelled")
	})

	t.Run("limit clamped to max", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT&limit=9999", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.EqualValues(t, meHistoryMaxLimit, resp.Limit)
	})

	t.Run("limit defaults when absent", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.EqualValues(t, meHistoryDefaultLimit, resp.Limit)
	})

	t.Run("page defaults to 1 when absent or invalid", func(t *testing.T) {
		t.Parallel()
		h := newH(t, &mockMeChartService{history: emptyHistoryResult})
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT&page=bad", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var resp dto.MeHistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.EqualValues(t, 1, resp.Page)
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
		h, err := NewHandler(&mockRateService{}, "token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, svc)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(42)
		h.logger = testLogger

		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT", nil)
		req.Header.Set("X-Telegram-Init-Data", secretInitData)
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		assert.NotContains(t, logBuf.String(), secretInitData, "handler must not log the X-Telegram-Init-Data value")
	})

	t.Run("forwards source_title to service", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{history: emptyHistoryResult}
		h := newH(t, svc)
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT&source_title=Kaspi", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "Kaspi", svc.received.sourceTitle)
	})

	t.Run("omits source_title when query param absent", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{history: emptyHistoryResult}
		h := newH(t, svc)
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "", svc.received.sourceTitle)
	})

	t.Run("trims whitespace around source_title", func(t *testing.T) {
		t.Parallel()
		svc := &mockMeChartService{history: emptyHistoryResult}
		h := newH(t, svc)
		req := httptest.NewRequest(http.MethodGet, "/api/me/rates/history?pair=USD/KZT&source_title=+Kaspi+", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GetMeRatesHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "Kaspi", svc.received.sourceTitle)
	})
}

func TestHandler_ListMeSubscriptionsRaw(t *testing.T) {
	t.Parallel()

	const callerID = int64(555)
	callerIDStr := strconv.FormatInt(callerID, 10)

	srcA := &domain.RateSource{Name: "src_a", Title: "Alpha", BaseCurrency: "USD", QuoteCurrency: "KZT"}
	srcB := &domain.RateSource{Name: "src_b", Title: "Beta", BaseCurrency: "EUR", QuoteCurrency: "KZT"}

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	makeSub := func(id, srcName, ct, cv string, updAt time.Time) domain.RateUserSubscription {
		return domain.RateUserSubscription{
			ID:             id,
			UserType:       domain.UserTypeTelegram,
			UserID:         callerIDStr,
			SourceName:     srcName,
			ConditionType:  domain.SubscriptionConditionType(ct),
			ConditionValue: cv,
			UpdatedAt:      updAt,
		}
	}

	t.Run("401 on missing initData", func(t *testing.T) {
		t.Parallel()
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		rr := httptest.NewRecorder()
		h.ListMeSubscriptionsRaw(rr, httptest.NewRequest(http.MethodGet, "/api/me/subscriptions/raw", nil))
		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Contains(t, rr.Body.String(), "unauthorized")
	})

	t.Run("200 empty items when user has no subscriptions", func(t *testing.T) {
		t.Parallel()
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions/raw", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptionsRaw(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeSubscriptionsRawResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.NotNil(t, body.Items)
		assert.Empty(t, body.Items)
	})

	t.Run("200 happy path returns per-condition rows with source metadata", func(t *testing.T) {
		t.Parallel()
		subRepo := &mockMeSubRepo{
			subs: map[string][]domain.RateUserSubscription{
				callerIDStr: {
					makeSub("id-1", "src_a", "delta", "0.5", now),
					makeSub("id-2", "src_b", "interval", "1h", now.Add(-time.Hour)),
				},
			},
		}
		sourceRepo := &mockMeSourceRepo{
			sources: map[string]*domain.RateSource{"src_a": srcA, "src_b": srcB},
		}

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions/raw", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptionsRaw(rr, req)

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
		assert.NotEmpty(t, body.Items[0].UpdatedAt)
	})

	t.Run("items sorted source_name ASC updated_at DESC", func(t *testing.T) {
		t.Parallel()
		sub1 := makeSub("z1", "src_b", "delta", "1", now)
		sub2 := makeSub("z2", "src_a", "delta", "2", now.Add(-time.Hour))
		sub3 := makeSub("z3", "src_a", "interval", "1h", now) // newer within src_a
		subRepo := &mockMeSubRepo{
			subs: map[string][]domain.RateUserSubscription{
				callerIDStr: {sub1, sub2, sub3},
			},
		}
		sourceRepo := &mockMeSourceRepo{
			sources: map[string]*domain.RateSource{"src_a": srcA, "src_b": srcB},
		}

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions/raw", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptionsRaw(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		var body dto.MeSubscriptionsRawResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body.Items, 3)

		// src_a rows come before src_b (ASC), and within src_a the newer row first (DESC).
		assert.Equal(t, "src_a", body.Items[0].SourceName)
		assert.Equal(t, "z3", body.Items[0].ID, "newer src_a row must come first")
		assert.Equal(t, "src_a", body.Items[1].SourceName)
		assert.Equal(t, "z2", body.Items[1].ID)
		assert.Equal(t, "src_b", body.Items[2].SourceName)
	})

	t.Run("500 on repo failure", func(t *testing.T) {
		t.Parallel()
		subRepo := &mockMeSubRepo{err: errors.New("db down")}

		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)
		h.logger = log.New(log.Writer(), "", 0) // suppress output in test run

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions/raw", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptionsRaw(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})
}

func TestHandler_CreateMeSubscription(t *testing.T) {
	t.Parallel()

	const callerID = int64(42)
	callerIDStr := strconv.FormatInt(callerID, 10)

	validSrc := &domain.RateSource{
		Name:          "src_a",
		Title:         "Source A",
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
		Active:        true,
	}

	newReq := func(body string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/me/subscriptions", strings.NewReader(body))
		req.Header.Set("X-Telegram-Init-Data", "valid")
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	t.Run("201 created with generated ID", func(t *testing.T) {
		t.Parallel()

		subRepo := &mockMeSubRepo{}
		sourceRepo := &mockMeSourceRepo{sources: map[string]*domain.RateSource{"src_a": validSrc}}

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, newReq(`{"source_name":"src_a","condition_type":"delta","condition_value":"5"}`))

		require.Equal(t, http.StatusCreated, rr.Code)
		var resp dto.MeSubscriptionCreateResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
		assert.NotEmpty(t, resp.ID)

		require.Len(t, subRepo.retained, 1)
		assert.Equal(t, callerIDStr, subRepo.retained[0].UserID)
		assert.Equal(t, domain.UserTypeTelegram, subRepo.retained[0].UserType)
		assert.Equal(t, "src_a", subRepo.retained[0].SourceName)
		assert.Equal(t, domain.ConditionTypeDelta, subRepo.retained[0].ConditionType)
		assert.Equal(t, "5", subRepo.retained[0].ConditionValue)
	})

	t.Run("401 on missing initData", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodPost, "/api/me/subscriptions", strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Contains(t, rr.Body.String(), "unauthorized")
	})

	t.Run("400 on malformed JSON body", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, newReq(`not-json`))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid request body")
	})

	t.Run("400 on unknown source", func(t *testing.T) {
		t.Parallel()

		sourceRepo := &mockMeSourceRepo{sources: map[string]*domain.RateSource{}} // source not present
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, newReq(`{"source_name":"no_such","condition_type":"delta","condition_value":"5"}`))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "unknown source")
	})

	t.Run("400 on invalid condition type", func(t *testing.T) {
		t.Parallel()

		sourceRepo := &mockMeSourceRepo{sources: map[string]*domain.RateSource{"src_a": validSrc}}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, newReq(`{"source_name":"src_a","condition_type":"bogus","condition_value":"5"}`))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid condition")
	})

	t.Run("400 on invalid condition value delta", func(t *testing.T) {
		t.Parallel()

		sourceRepo := &mockMeSourceRepo{sources: map[string]*domain.RateSource{"src_a": validSrc}}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, newReq(`{"source_name":"src_a","condition_type":"delta","condition_value":"not-a-number"}`))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid condition")
	})

	t.Run("400 on invalid condition value interval", func(t *testing.T) {
		t.Parallel()

		sourceRepo := &mockMeSourceRepo{sources: map[string]*domain.RateSource{"src_a": validSrc}}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, newReq(`{"source_name":"src_a","condition_type":"interval","condition_value":"30s"}`))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid condition")
	})

	t.Run("400 on invalid condition value daily", func(t *testing.T) {
		t.Parallel()

		sourceRepo := &mockMeSourceRepo{sources: map[string]*domain.RateSource{"src_a": validSrc}}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, newReq(`{"source_name":"src_a","condition_type":"daily","condition_value":"not-a-time"}`))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid condition")
	})

	t.Run("500 on source repo failure", func(t *testing.T) {
		t.Parallel()

		sourceRepo := &mockMeSourceRepo{err: errors.New("db down")}
		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)
		h.logger = log.New(log.Writer(), "", 0)

		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, newReq(`{"source_name":"src_a","condition_type":"delta","condition_value":"5"}`))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})

	t.Run("500 on retain repo failure", func(t *testing.T) {
		t.Parallel()

		subRepo := &mockMeSubRepo{retainErr: errors.New("db down")}
		sourceRepo := &mockMeSourceRepo{sources: map[string]*domain.RateSource{"src_a": validSrc}}

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)
		h.logger = log.New(log.Writer(), "", 0)

		rr := httptest.NewRecorder()
		h.CreateMeSubscription(rr, newReq(`{"source_name":"src_a","condition_type":"delta","condition_value":"5"}`))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})
}

func TestHandler_UpdateMeSubscription(t *testing.T) {
	t.Parallel()

	const callerID = int64(10)
	const otherID = int64(20)
	callerIDStr := strconv.FormatInt(callerID, 10)
	otherIDStr := strconv.FormatInt(otherID, 10)

	existingSub := &domain.RateUserSubscription{
		ID:             "sub-001",
		UserType:       domain.UserTypeTelegram,
		UserID:         callerIDStr,
		SourceName:     "src_a",
		ConditionType:  domain.ConditionTypeDelta,
		ConditionValue: "5",
	}

	newReq := func(id, body string) *http.Request {
		req := httptest.NewRequest(http.MethodPatch, "/api/me/subscriptions/"+id, strings.NewReader(body))
		req.Header.Set("X-Telegram-Init-Data", "valid")
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", id)
		return req
	}

	t.Run("204 on successful update", func(t *testing.T) {
		t.Parallel()

		sub := *existingSub
		subRepo := &mockMeSubRepo{
			byID: map[string]*domain.RateUserSubscription{"sub-001": &sub},
		}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, newReq("sub-001", `{"condition_type":"interval","condition_value":"1h"}`))

		require.Equal(t, http.StatusNoContent, rr.Code)
		require.Len(t, subRepo.retained, 1)
		assert.Equal(t, domain.ConditionTypeInterval, subRepo.retained[0].ConditionType)
		assert.Equal(t, "1h", subRepo.retained[0].ConditionValue)
	})

	t.Run("401 on missing initData", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodPatch, "/api/me/subscriptions/sub-001", strings.NewReader(`{}`))
		req.SetPathValue("id", "sub-001")
		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Contains(t, rr.Body.String(), "unauthorized")
	})

	t.Run("404 on missing subscription", func(t *testing.T) {
		t.Parallel()

		subRepo := &mockMeSubRepo{byID: map[string]*domain.RateUserSubscription{}}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, newReq("no-such", `{"condition_type":"delta","condition_value":"1"}`))

		require.Equal(t, http.StatusNotFound, rr.Code)
		require.Contains(t, rr.Body.String(), "subscription not found")
	})

	t.Run("404 on cross-user access (ownership mismatch)", func(t *testing.T) {
		t.Parallel()

		otherSub := &domain.RateUserSubscription{
			ID:             "sub-other",
			UserType:       domain.UserTypeTelegram,
			UserID:         otherIDStr,
			SourceName:     "src_a",
			ConditionType:  domain.ConditionTypeDelta,
			ConditionValue: "3",
		}
		subRepo := &mockMeSubRepo{byID: map[string]*domain.RateUserSubscription{"sub-other": otherSub}}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID) // caller != owner

		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, newReq("sub-other", `{"condition_type":"delta","condition_value":"1"}`))

		require.Equal(t, http.StatusNotFound, rr.Code)
		require.Contains(t, rr.Body.String(), "subscription not found")
	})

	t.Run("400 on malformed body", func(t *testing.T) {
		t.Parallel()

		sub := *existingSub
		subRepo := &mockMeSubRepo{byID: map[string]*domain.RateUserSubscription{"sub-001": &sub}}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, newReq("sub-001", `not-json`))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid request body")
	})

	t.Run("400 on invalid condition", func(t *testing.T) {
		t.Parallel()

		sub := *existingSub
		subRepo := &mockMeSubRepo{byID: map[string]*domain.RateUserSubscription{"sub-001": &sub}}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, newReq("sub-001", `{"condition_type":"unknown","condition_value":"x"}`))

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid condition")
	})

	t.Run("500 on lookup failure", func(t *testing.T) {
		t.Parallel()

		subRepo := &mockMeSubRepo{err: errors.New("db down")}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)
		h.logger = log.New(log.Writer(), "", 0)

		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, newReq("sub-001", `{"condition_type":"delta","condition_value":"1"}`))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})

	t.Run("500 on retain failure", func(t *testing.T) {
		t.Parallel()

		sub := *existingSub
		subRepo := &mockMeSubRepo{
			byID:      map[string]*domain.RateUserSubscription{"sub-001": &sub},
			retainErr: errors.New("db down"),
		}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)
		h.logger = log.New(log.Writer(), "", 0)

		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, newReq("sub-001", `{"condition_type":"interval","condition_value":"1h"}`))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})

	t.Run("400 on body exceeding 4 KiB", func(t *testing.T) {
		t.Parallel()

		sub := *existingSub
		subRepo := &mockMeSubRepo{byID: map[string]*domain.RateUserSubscription{"sub-001": &sub}}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		// 5 KiB body — exceeds the 4 KiB MaxBytesReader limit.
		bigBody := strings.Repeat("x", 5<<10)
		req := httptest.NewRequest(http.MethodPatch, "/api/me/subscriptions/sub-001", strings.NewReader(bigBody))
		req.Header.Set("X-Telegram-Init-Data", "valid")
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", "sub-001")

		rr := httptest.NewRecorder()
		h.UpdateMeSubscription(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
		require.Contains(t, rr.Body.String(), "invalid request body")
	})
}

func TestHandler_DeleteMeSubscription(t *testing.T) {
	t.Parallel()

	const callerID = int64(10)
	const otherID = int64(20)
	callerIDStr := strconv.FormatInt(callerID, 10)
	otherIDStr := strconv.FormatInt(otherID, 10)

	existingSub := &domain.RateUserSubscription{
		ID:             "sub-001",
		UserType:       domain.UserTypeTelegram,
		UserID:         callerIDStr,
		SourceName:     "src_a",
		ConditionType:  domain.ConditionTypeDelta,
		ConditionValue: "5",
	}

	newReq := func(id string) *http.Request {
		req := httptest.NewRequest(http.MethodDelete, "/api/me/subscriptions/"+id, nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		req.SetPathValue("id", id)
		return req
	}

	t.Run("204 on successful delete", func(t *testing.T) {
		t.Parallel()

		sub := *existingSub
		subRepo := &mockMeSubRepo{byID: map[string]*domain.RateUserSubscription{"sub-001": &sub}}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, newReq("sub-001"))

		require.Equal(t, http.StatusNoContent, rr.Code)
		require.Len(t, subRepo.removed, 1)
		assert.Equal(t, "sub-001", subRepo.removed[0].ID)
	})

	t.Run("401 on missing initData", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodDelete, "/api/me/subscriptions/sub-001", nil)
		req.SetPathValue("id", "sub-001")
		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Contains(t, rr.Body.String(), "unauthorized")
	})

	t.Run("404 on missing subscription", func(t *testing.T) {
		t.Parallel()

		subRepo := &mockMeSubRepo{byID: map[string]*domain.RateUserSubscription{}}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, newReq("no-such"))

		require.Equal(t, http.StatusNotFound, rr.Code)
		require.Contains(t, rr.Body.String(), "subscription not found")
	})

	t.Run("404 on cross-user access (ownership mismatch)", func(t *testing.T) {
		t.Parallel()

		otherSub := &domain.RateUserSubscription{
			ID:             "sub-other",
			UserType:       domain.UserTypeTelegram,
			UserID:         otherIDStr,
			SourceName:     "src_a",
			ConditionType:  domain.ConditionTypeDelta,
			ConditionValue: "3",
		}
		subRepo := &mockMeSubRepo{byID: map[string]*domain.RateUserSubscription{"sub-other": otherSub}}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)

		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, newReq("sub-other"))

		require.Equal(t, http.StatusNotFound, rr.Code)
		require.Contains(t, rr.Body.String(), "subscription not found")
	})

	t.Run("500 on lookup failure", func(t *testing.T) {
		t.Parallel()

		subRepo := &mockMeSubRepo{err: errors.New("db down")}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)
		h.logger = log.New(log.Writer(), "", 0)

		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, newReq("sub-001"))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})

	t.Run("500 on remove failure", func(t *testing.T) {
		t.Parallel()

		sub := *existingSub
		subRepo := &mockMeSubRepo{
			byID:      map[string]*domain.RateUserSubscription{"sub-001": &sub},
			removeErr: errors.New("db down"),
		}
		h, err := NewHandler(&mockRateService{}, "", subRepo, &mockMeSourceRepo{}, &mockMeRateValueRepo{}, &mockMeProfileRepo{}, nil)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerID)
		h.logger = log.New(log.Writer(), "", 0)

		rr := httptest.NewRecorder()
		h.DeleteMeSubscription(rr, newReq("sub-001"))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
		require.Contains(t, rr.Body.String(), "internal error")
	})
}
