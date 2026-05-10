package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/repository"
	"github.com/seilbekskindirov/monitor/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ meSubscriptionRepository = (*mockMeSubRepo)(nil)
var _ meSourceRepository = (*mockMeSourceRepo)(nil)
var _ meRateValueRepository = (*mockMeRateValueRepo)(nil)

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

		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []api.SourceResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.Equal(t, "src1", body[0].Name)
	})

	t.Run("200 empty array when no sources", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{sources: nil}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusOK, rr.Code)

		var body []api.SourceResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Empty(t, body)
	})

	t.Run("500 on ObtainAllRateSources error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSources(rr, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/rates", nil)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListRates(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []api.RateResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
	})

	t.Run("200 empty", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{rates: nil}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/rates", nil)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListRates(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body []api.RateResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Empty(t, body)
	})

	t.Run("400 when name path param missing", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListRates(rr, httptest.NewRequest(http.MethodGet, "/api/sources//rates", nil))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/history", nil)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListHistory(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []api.HistoryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 3)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListNotifications(rr, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []api.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.NotEmpty(t, body[0].ID)
		require.NotEmpty(t, body[0].UserType)
		require.NotEmpty(t, body[0].Status)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListFailedNotifications(rr, httptest.NewRequest(http.MethodGet, "/?offset=50&limit=20", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []api.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
	})

	t.Run("200 with no params returns default page", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{events: []domain.RateUserEvent{}}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListFailedNotifications(rr, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListPendingEvents(rr, httptest.NewRequest(http.MethodGet, "/api/events/pending", nil))

		require.Equal(t, http.StatusOK, rr.Code)

		var body []api.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
		require.Empty(t, body[0].UserID, "user_id must be omitted")
	})

	t.Run("200 empty array when none pending", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{events: []domain.RateUserEvent{}}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListPendingEvents(rr, httptest.NewRequest(http.MethodGet, "/api/events/pending", nil))

		require.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListPendingEvents(rr, httptest.NewRequest(http.MethodGet, "/api/events/pending", nil))

		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestGetRatesChart(t *testing.T) {
	t.Parallel()

	t.Run("200 with chart points", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{
			chartPoints: []repository.ChartPoint{
				{Label: "2026-04-01", Price: 450.12},
				{Label: "2026-04-02", Price: 451.00},
			},
		}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/rates/chart?period=week", nil)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.GetRatesChart(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body []api.ChartPointResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.Equal(t, "2026-04-01", body[0].Label)
	})

	t.Run("400 when name missing", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.GetRatesChart(rr, httptest.NewRequest(http.MethodGet, "/api/sources//rates/chart", nil))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/rates/chart", nil)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.GetRatesChart(rr, req)

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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/events/failed?page=1", nil)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceFailedEvents(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body []api.NotificationResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
		require.Empty(t, body[0].UserID, "user_id must not be present")
	})

	t.Run("400 when name missing", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSourceFailedEvents(rr, httptest.NewRequest(http.MethodGet, "/api/sources//events/failed", nil))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/subscriptions", nil)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceSubscriptions(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body []api.SubscriptionSummaryResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 1)
		require.Equal(t, "src1", body[0].SourceName)
		require.Empty(t, body[0].LastSentAt, "last_sent_at must be omitted when zero")
	})

	t.Run("400 when name missing", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSourceSubscriptions(rr, httptest.NewRequest(http.MethodGet, "/api/sources//subscriptions", nil))
		require.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		svc := &mockRateService{err: errors.New("db error")}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPatch, "/api/sources/src1/active", strings.NewReader(`{"active":true}`))
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code)
	})
	t.Run("404 when source not found", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: internal.ErrNotFound}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPatch, "/api/sources/unknown/active", strings.NewReader(`{"active":true}`))
		req.SetPathValue("name", "unknown")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusNotFound, rr.Code)
	})
	t.Run("400 on malformed request body", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPatch, "/api/sources/src1/active", strings.NewReader(`not-json`))
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("400 when name path param missing", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ToggleSourceActive(rr, httptest.NewRequest(http.MethodPatch, "/api/sources//active", strings.NewReader(`{"active":true}`)))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("500 on unexpected service error", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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

		svc := &mockRateService{stats: repository.StatsResult{SourcesTotal: 5, SourcesActive: 3, ErrorsTotal: 7}}
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListStats(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body api.StatsResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, int64(5), body.SourcesTotal)
		require.Equal(t, int64(3), body.SourcesActive)
		require.Equal(t, int64(7), body.ErrorsTotal)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/subscriptions/list?page=1", nil)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceSubscriptionDetails(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []api.SubscriptionDetailResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.Equal(t, "sub1", body[0].ID)
		require.NotEmpty(t, body[0].LatestNotifiedAt, "latest_notified_at must be populated when non-zero")
		require.Empty(t, body[1].LatestNotifiedAt, "latest_notified_at must be omitted when zero")
	})
	t.Run("400 when name path param missing", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSourceSubscriptionDetails(rr, httptest.NewRequest(http.MethodGet, "/api/sources//subscriptions/list", nil))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/sources/src1/events/daily?page=1", nil)
		req.SetPathValue("name", "src1")
		rr := httptest.NewRecorder()
		h.ListSourceDailyEvents(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []api.DailyEventResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.Equal(t, "2026-04-12", body[0].Date)
		require.Equal(t, int64(10), body[0].SuccessCount)
	})
	t.Run("400 when name path param missing", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListSourceDailyEvents(rr, httptest.NewRequest(http.MethodGet, "/api/sources//events/daily", nil))

		require.Equal(t, http.StatusBadRequest, rr.Code)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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
		h, err := NewHandler(svc, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListExecutionErrors(rr, httptest.NewRequest(http.MethodGet, "/api/errors/execution?page=1", nil))

		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		var body []api.ExecutionErrorResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Len(t, body, 2)
		require.Equal(t, "h1", body[0].ID)
		require.Equal(t, "src1", body[0].SourceName)
		require.Equal(t, "timeout", body[0].Error)
		require.NotEmpty(t, body[0].Timestamp)
	})
	t.Run("200 empty array on page with no records", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{historyItems: nil}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		h.ListExecutionErrors(rr, httptest.NewRequest(http.MethodGet, "/api/errors/execution?page=99", nil))

		require.Equal(t, http.StatusOK, rr.Code)

		var body []api.ExecutionErrorResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Empty(t, body)
	})
	t.Run("500 on error", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{err: errors.New("db error")}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
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
	chartPoints           []repository.ChartPoint
	subscriptionSummaries []domain.RateUserSubscriptionSummary
	subscriptionDetails   []domain.RateUserSubscriptionDetail
	dailySummaries        []domain.RateUserEventDailySummary
	stats                 repository.StatsResult
	err                   error
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

func (m *mockRateService) ObtainRateValueChartBySourceName(_ context.Context, _ string, _ repository.ChartPeriod) ([]repository.ChartPoint, error) {
	return m.chartPoints, m.err
}

func (m *mockRateService) ObtainFailedRateUserEventsBySourceName(_ context.Context, _ string, _, _ int64) ([]domain.RateUserEvent, error) {
	return m.events, m.err
}

func (m *mockRateService) ObtainSubscriptionSummaryBySource(_ context.Context, _ string) ([]domain.RateUserSubscriptionSummary, error) {
	return m.subscriptionSummaries, m.err
}

func (m *mockRateService) ObtainStats(_ context.Context) (repository.StatsResult, error) {
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
	subs map[string][]domain.RateUserSubscription
	err  error
}

func (m *mockMeSubRepo) ObtainRateUserSubscriptionsByUserID(_ context.Context, _ domain.UserType, userID string) ([]domain.RateUserSubscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.subs[userID], nil
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

// mockMeRateValueRepo is a test double for meRateValueRepository.
type mockMeRateValueRepo struct {
	rates map[string][]domain.RateValue
	err   error
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
	const otherUserID = int64(222)
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

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil))

		require.Equal(t, http.StatusUnauthorized, rr.Code)
		require.Contains(t, rr.Body.String(), "unauthorized")
	})

	t.Run("rejects bad hash with 401", func(t *testing.T) {
		t.Parallel()

		h, err := NewHandler(&mockRateService{}, "", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{})
		require.NoError(t, err)
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil)
		req.Header.Set("X-Telegram-Init-Data", "hash=badvalue&auth_date=1234")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code)
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

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, rateRepo)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body api.MeSubscriptionsResponse
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

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, rateRepo)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions?q=euro", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body api.MeSubscriptionsResponse
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

		h, err := NewHandler(&mockRateService{}, "", subRepo, sourceRepo, rateRepo)
		require.NoError(t, err)
		h.validateInitData = alwaysValidateInitData(callerUserID)

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions?page=2&page_size=10", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.ListMeSubscriptions(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)

		var body api.MeSubscriptionsResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		require.Equal(t, int64(12), body.Total)
		require.Len(t, body.Items, 2, "page 2 of 10-per-page with 12 items should return 2")
		assert.Equal(t, int64(2), body.Page)
		assert.Equal(t, int64(10), body.PageSize)
	})
}
