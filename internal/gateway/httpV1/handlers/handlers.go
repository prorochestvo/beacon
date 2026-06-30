// Package handlers contains the HTTP handler implementations for the v1 API.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	appchart "github.com/seilbekskindirov/beacon/internal/application/chart"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
	"github.com/seilbekskindirov/beacon/internal/tools/tgwebapp"
)

// NewHandler constructs a Handler wired to the rate service and, optionally,
// the Mini App auth dependencies. botToken, meSubRepo, meSourceRepo, and
// meRateValueRepo are required for ListMeSubscriptions; meProfileRepo for
// UpsertMeProfile; remaining handlers need only srvRate. meChartSvc is required
// for GetMeRatesChart and GetPublicRatesChart and may be nil (those return 503).
// healthAgent drives GET /health/check; when nil the endpoint returns 503.
// serverVersion and serverStart populate the "server" block in the health response.
func NewHandler(
	srvRate rateService,
	botToken string,
	meSubRepo meSubscriptionRepository,
	meSourceRepo meSourceRepository,
	meRateValueRepo meRateValueRepository,
	meProfileRepo meProfileRepository,
	meChartSvc meChartService,
	healthAgent healthCheckAgent,
	serverVersion string,
	serverStart time.Time,
) (*Handler, error) {
	h := &Handler{
		rateService:      srvRate,
		botToken:         botToken,
		meSubRepo:        meSubRepo,
		meSourceRepo:     meSourceRepo,
		meRateValueRepo:  meRateValueRepo,
		meProfileRepo:    meProfileRepo,
		meChartSvc:       meChartSvc,
		healthAgent:      healthAgent,
		serverVersion:    serverVersion,
		serverStart:      serverStart,
		validateInitData: tgwebapp.ValidateInitData,
		nowFn:            time.Now,
		logger:           log.Default(),
	}
	return h, nil
}

// Handler groups all v1 HTTP handlers and their repository dependencies.
type Handler struct {
	rateService
	botToken        string
	meSubRepo       meSubscriptionRepository
	meSourceRepo    meSourceRepository
	meRateValueRepo meRateValueRepository
	meProfileRepo   meProfileRepository
	meChartSvc      meChartService
	healthAgent     healthCheckAgent
	serverVersion   string
	serverStart     time.Time

	// Weather city endpoints — nil when not wired; handlers return 503 in that case.
	meWeatherCityRepo meWeatherCityRepository
	weatherGeocoder   weatherGeocoder
	// meWeatherObsRepo is required by GetMeWeatherCurrent; nil until WithWeatherObsRepo is called.
	meWeatherObsRepo meWeatherObsRepository

	// validateInitData is the Telegram WebApp initData verifier. A field so tests
	// can inject a fake without real bot tokens.
	validateInitData func(initData, botToken string, maxAge time.Duration, now time.Time) (int64, error)
	// nowFn returns the current time. Injected for deterministic tests.
	nowFn func() time.Time
	// logger is used by internalError. Defaults to log.Default() so tests can
	// inject a per-test logger without touching the global writer.
	logger *log.Logger
}

type rateService interface {
	ObtainLastNExecutionHistoryBySourceName(ctx context.Context, name string, limit int64) ([]domain.ExecutionHistory, error)
	ObtainLatestExecutionHistoryBySources(ctx context.Context, names []string) (map[string]domain.ExecutionHistory, error)
	ObtainLastSuccessNExecutionHistoryBySourceName(ctx context.Context, name string, limit int64) ([]domain.ExecutionHistory, error)
	ObtainAllRateSources(ctx context.Context) ([]domain.RateSource, error)
	UpdateRateSourceActive(ctx context.Context, name string, active bool) error
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, limit int64) ([]domain.RateValue, error)
	ObtainListOfLastRateUserEvent(ctx context.Context, limit int64) ([]domain.RateUserEvent, error)
	ObtainFailedListOfRateUserEvent(ctx context.Context, offset, limit int64) ([]domain.RateUserEvent, error)
	ObtainPendingRateUserEvents(ctx context.Context) ([]domain.RateUserEvent, error)
	ObtainFailedRateUserEventsBySourceName(ctx context.Context, sourceName string, page, pageSize int64) ([]domain.RateUserEvent, error)
	ObtainSubscriptionSummaryBySource(ctx context.Context, sourceName string) ([]domain.RateUserSubscriptionSummary, error)
	ObtainStats(ctx context.Context) (domain.StatsResult, error)
	ObtainRateUserSubscriptionsBySourcePaged(ctx context.Context, sourceName string, offset, limit int64) ([]domain.RateUserSubscriptionDetail, error)
	ObtainDailyEventSummaryBySource(ctx context.Context, sourceName string, offset, limit int64) ([]domain.RateUserEventDailySummary, error)
	ObtainLastNExecutionHistoryErrors(ctx context.Context, offset, limit int64) ([]domain.ExecutionHistory, error)
}

type meSubscriptionRepository interface {
	ObtainRateUserSubscriptionsByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error)
	ObtainRateUserSubscriptionByID(ctx context.Context, id string) (*domain.RateUserSubscription, error)
	RetainRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error
	RemoveRateUserSubscription(ctx context.Context, record *domain.RateUserSubscription) error
}

type meSourceRepository interface {
	ObtainRateSourceByName(ctx context.Context, name string) (*domain.RateSource, error)
	ObtainRateSourcesByNames(ctx context.Context, names []string) (map[string]domain.RateSource, error)
}

type meRateValueRepository interface {
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, limit int64) ([]domain.RateValue, error)
	ObtainLatestRateValuesBySourceNames(ctx context.Context, names []string) (map[string]domain.RateValue, error)
}

type meProfileRepository interface {
	UpsertRateUserProfile(ctx context.Context, record *domain.RateUserProfile) error
}

// meChartService is the application service contract consumed by GetMeRatesChart,
// GetMeRatesHistory, and GetPublicRatesChart, satisfied by *appchart.Service.
// Only the period-aware variants are listed; the default-period wrappers
// (ObtainMeChart, ObtainPublicChart) exist on the concrete type but not here.
// healthCheckAgent is the contract for the health-check aggregator. CheckUp probes
// all registered dependencies under a bounded timeout and returns a per-component
// report; healthy is true iff every component reported nil. Nil is allowed (the
// HealthCheck handler returns 503 when the agent is not wired).
type healthCheckAgent interface {
	CheckUp(ctx context.Context) (healthy bool, report map[string]string)
}

type meChartService interface {
	ObtainMeChartForPeriod(ctx context.Context, userID string, periodDays int64) (*appchart.MeChart, error)
	ObtainMeHistory(ctx context.Context, userID, pair, sourceTitle string, page, limit int64) (*appchart.MeHistoryResult, error)
	ObtainPublicChartForPeriod(ctx context.Context, page, limit, periodDays int64) (*appchart.PublicChart, int64, error)
}

// Ping is the liveness probe: it always returns 200 and touches no dependency.
// Registered at both GET /ping and GET /healthz (backward-compatibility alias).
//
// GET /ping
// GET /healthz
func (h *Handler) Ping(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// HealthCheck is the readiness probe. It runs all registered dependency inspectors
// under a bounded timeout and returns a per-component JSON report. 200 when all
// dependencies are healthy; 503 when any are down (the body still lists every
// component so operators can see which one failed). No auth; for deploy gates and
// uptime monitors.
//
// GET /health/check
func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	if h.healthAgent == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":false,"server":{},"services":{}}`))
		return
	}

	healthy, report := h.healthAgent.CheckUp(r.Context())

	var uptime string
	if !h.serverStart.IsZero() {
		uptime = time.Since(h.serverStart).Truncate(time.Second).String()
	}

	body := dto.HealthCheckResponse{
		Status: healthy,
		Server: dto.HealthServer{
			Version: h.serverVersion,
			Uptime:  uptime,
		},
		Services: report,
	}

	status := http.StatusOK
	if !healthy {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		h.logger.Print(errors.Join(
			fmt.Errorf("encode health check response: %w", err),
			internal.NewTraceError(),
		))
	}
}

// ListSources returns every configured rate source decorated with its latest execution status.
//
// GET /api/sources
func (h *Handler) ListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := h.rateService.ObtainAllRateSources(r.Context())
	if err != nil {
		h.internalError(w, err)
		return
	}

	// Bulk-load the latest execution_history row per source so the response
	// loop is O(1) per source instead of one DB transaction each (the previous
	// N+1 pattern). A bulk failure is logged but the loop still emits source
	// rows without execution fields populated.
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Name)
	}
	latest, latestErr := h.rateService.ObtainLatestExecutionHistoryBySources(r.Context(), names)
	if latestErr != nil {
		log.Print(errors.Join(
			fmt.Errorf("bulk latest execution: %w", latestErr),
			internal.NewTraceError(),
		))
		latest = map[string]domain.ExecutionHistory{}
	}

	resp := make([]dto.SourceResponse, 0, len(sources))
	for _, s := range sources {
		item := dto.SourceResponse{
			Name:          s.Name,
			Title:         s.Title,
			BaseCurrency:  s.BaseCurrency,
			QuoteCurrency: s.QuoteCurrency,
			Interval:      s.Interval,
			Active:        s.Active,
		}
		if rec, ok := latest[s.Name]; ok {
			item.LastSuccess = rec.Success
			item.LastError = rec.Error
			item.LastRunAt = rec.Timestamp.Format(time.RFC3339)
		}
		resp = append(resp, item)
	}
	writeJSON(w, resp)
}

// ListRates returns the most recent rate values for a named source.
// Optional query param ?limit=N (1–1000, default 100).
//
// GET /api/sources/{name}/rates
func (h *Handler) ListRates(w http.ResponseWriter, r *http.Request) {
	name, err := extractName(r)
	if err != nil {
		http.Error(w, `{"error":"missing source name"}`, http.StatusBadRequest)
		return
	}
	limit, err := extractLimit(r.URL)
	if err != nil {
		http.Error(w, `{"error":"limit must be a number"}`, http.StatusBadRequest)
		return
	}

	rates, err := h.rateService.ObtainLastNRateValuesBySourceName(r.Context(), name, limit)
	if err != nil {
		h.internalError(w, err)
		return
	}

	resp := make([]dto.RateResponse, 0, len(rates))
	for _, rv := range rates {
		resp = append(resp, dto.RateResponse{
			ID:            rv.ID,
			Price:         rv.Price,
			BaseCurrency:  rv.BaseCurrency,
			QuoteCurrency: rv.QuoteCurrency,
			Timestamp:     rv.Timestamp.Format(time.RFC3339),
		})
	}
	writeJSON(w, resp)
}

// ListHistory returns the 50 most recent execution history records for a named source.
//
// GET /api/sources/{name}/history
func (h *Handler) ListHistory(w http.ResponseWriter, r *http.Request) {
	limit, err := extractLimit(r.URL)
	if err != nil {
		http.Error(w, `{"error":"limit must be a number"}`, http.StatusBadRequest)
		return
	}
	name, err := extractName(r)
	if err != nil {
		http.Error(w, `{"error":"missing source name"}`, http.StatusBadRequest)
		return
	}
	recs, err := h.rateService.ObtainLastNExecutionHistoryBySourceName(r.Context(), name, limit)
	if err != nil {
		h.internalError(w, err)
		return
	}

	resp := make([]dto.HistoryResponse, 0, len(recs))
	for _, rec := range recs {
		resp = append(resp, dto.HistoryResponse{
			ID:         rec.ID,
			SourceName: rec.SourceName,
			Success:    rec.Success,
			Error:      rec.Error,
			Timestamp:  rec.Timestamp.Format(time.RFC3339),
		})
	}
	writeJSON(w, resp)
}

// ListNotifications returns the last N notification pool records.
// Optional query param ?limit=N (1–100, default 10).
//
// GET /api/notifications
func (h *Handler) ListNotifications(w http.ResponseWriter, r *http.Request) {
	limit, err := extractLimit(r.URL)
	if err != nil {
		http.Error(w, `{"error":"limit must be a number"}`, http.StatusBadRequest)
		return
	}

	records, err := h.rateService.ObtainListOfLastRateUserEvent(r.Context(), limit)
	if err != nil {
		h.internalError(w, err)
		return
	}

	resp := make([]dto.NotificationResponse, 0, len(records))
	for _, rec := range records {
		resp = append(resp, dto.NotificationResponse{
			ID:        rec.ID,
			UserType:  string(rec.UserType),
			UserID:    rec.UserID,
			Status:    string(rec.Status),
			LastError: rec.LastError,
			CreatedAt: rec.CreatedAt,
			SentAt:    rec.SentAt,
		})
	}
	writeJSON(w, resp)
}

// ListFailedNotifications returns all failed notification pool records.
//
// GET /api/notifications/failed
func (h *Handler) ListFailedNotifications(w http.ResponseWriter, r *http.Request) {
	limit, err := extractLimit(r.URL)
	if err != nil {
		http.Error(w, `{"error":"limit must be a number"}`, http.StatusBadRequest)
		return
	}
	offset, err := extractOffset(r)
	if err != nil {
		http.Error(w, `{"error":"offset must be a number"}`, http.StatusBadRequest)
		return
	}

	records, err := h.rateService.ObtainFailedListOfRateUserEvent(r.Context(), offset, limit)
	if err != nil {
		h.internalError(w, err)
		return
	}

	resp := make([]dto.NotificationResponse, 0, len(records))
	for _, rec := range records {
		resp = append(resp, dto.NotificationResponse{
			ID:        rec.ID,
			UserType:  string(rec.UserType),
			UserID:    rec.UserID,
			Status:    string(rec.Status),
			LastError: rec.LastError,
			CreatedAt: rec.CreatedAt,
			SentAt:    rec.SentAt,
		})
	}
	writeJSON(w, resp)
}

// ListPendingEvents returns all currently pending notification events.
//
// GET /api/events/pending
func (h *Handler) ListPendingEvents(w http.ResponseWriter, r *http.Request) {
	events, err := h.rateService.ObtainPendingRateUserEvents(r.Context())
	if err != nil {
		h.internalError(w, err)
		return
	}
	resp := make([]dto.NotificationResponse, 0, len(events))
	for _, e := range events {
		resp = append(resp, dto.NotificationResponse{
			ID:        e.ID,
			UserType:  string(e.UserType),
			Status:    string(e.Status),
			CreatedAt: e.CreatedAt,
		})
	}
	writeJSON(w, resp)
}

// ListSourceFailedEvents returns paginated failed events for a named source.
//
// GET /api/sources/{name}/events/failed?page=N
func (h *Handler) ListSourceFailedEvents(w http.ResponseWriter, r *http.Request) {
	name, err := extractName(r)
	if err != nil {
		http.Error(w, `{"error":"missing source name"}`, http.StatusBadRequest)
		return
	}
	page := parsePage(r.URL.Query().Get("page"))
	const pageSize = 50
	events, err := h.rateService.ObtainFailedRateUserEventsBySourceName(r.Context(), name, page, pageSize)
	if err != nil {
		h.internalError(w, err)
		return
	}
	resp := make([]dto.NotificationResponse, 0, len(events))
	for _, e := range events {
		resp = append(resp, dto.NotificationResponse{
			ID:        e.ID,
			UserType:  string(e.UserType),
			Status:    string(e.Status),
			LastError: e.LastError,
			CreatedAt: e.CreatedAt,
			SentAt:    e.SentAt,
		})
	}
	writeJSON(w, resp)
}

// ListSourceSubscriptions returns grouped subscription + event statistics for a source.
//
// GET /api/sources/{name}/subscriptions
func (h *Handler) ListSourceSubscriptions(w http.ResponseWriter, r *http.Request) {
	name, err := extractName(r)
	if err != nil {
		http.Error(w, `{"error":"missing source name"}`, http.StatusBadRequest)
		return
	}
	summaries, err := h.rateService.ObtainSubscriptionSummaryBySource(r.Context(), name)
	if err != nil {
		h.internalError(w, err)
		return
	}
	resp := make([]dto.SubscriptionSummaryResponse, 0, len(summaries))
	for _, s := range summaries {
		item := dto.SubscriptionSummaryResponse{
			SourceName:        s.SourceName,
			UserType:          string(s.UserType),
			SubscriptionCount: s.SubscriptionCount,
			SuccessCount:      s.SuccessCount,
			FailedCount:       s.FailedCount,
		}
		if !s.LastSentAt.IsZero() {
			item.LastSentAt = s.LastSentAt.Format(time.RFC3339)
		}
		resp = append(resp, item)
	}
	writeJSON(w, resp)
}

// ToggleSourceActive enables or disables a named source.
//
// PATCH /api/sources/{name}/active
func (h *Handler) ToggleSourceActive(w http.ResponseWriter, r *http.Request) {
	name, err := extractName(r)
	if err != nil {
		http.Error(w, `{"error":"missing source name"}`, http.StatusBadRequest)
		return
	}

	var body dto.SourceActiveRequest
	if err = json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if err = h.rateService.UpdateRateSourceActive(r.Context(), name, body.Active); err != nil {
		if errors.Is(err, internal.ErrNotFound) {
			http.Error(w, `{"error":"source not found"}`, http.StatusNotFound)
			return
		}
		h.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListStats returns global statistics: total/active source counts and total error count.
//
// GET /api/stats
func (h *Handler) ListStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.rateService.ObtainStats(r.Context())
	if err != nil {
		h.internalError(w, err)
		return
	}
	writeJSON(w, dto.StatsResponse{
		SourcesTotal:  stats.SourcesTotal,
		SourcesActive: stats.SourcesActive,
		ErrorsTotal:   stats.ErrorsTotal,
	})
}

// ListSourceSubscriptionDetails returns paginated subscription details for a named source.
//
// GET /api/sources/{name}/subscriptions/list?page=N
func (h *Handler) ListSourceSubscriptionDetails(w http.ResponseWriter, r *http.Request) {
	name, err := extractName(r)
	if err != nil {
		http.Error(w, `{"error":"missing source name"}`, http.StatusBadRequest)
		return
	}
	page := parsePage(r.URL.Query().Get("page"))
	const pageSize int64 = 25
	offset := (page - 1) * pageSize

	items, err := h.rateService.ObtainRateUserSubscriptionsBySourcePaged(r.Context(), name, offset, pageSize)
	if err != nil {
		h.internalError(w, err)
		return
	}
	resp := make([]dto.SubscriptionDetailResponse, 0, len(items))
	for _, s := range items {
		item := dto.SubscriptionDetailResponse{
			ID:         s.ID,
			UserType:   string(s.UserType),
			SourceName: s.SourceName,
			Condition:  s.ConditionType + ": " + s.ConditionValue,
		}
		if !s.LatestNotifiedAt.IsZero() {
			item.LatestNotifiedAt = s.LatestNotifiedAt.Format(time.RFC3339)
		}
		resp = append(resp, item)
	}
	writeJSON(w, resp)
}

// ListSourceDailyEvents returns paginated daily event summaries for a named source.
//
// GET /api/sources/{name}/events/daily?page=N
func (h *Handler) ListSourceDailyEvents(w http.ResponseWriter, r *http.Request) {
	name, err := extractName(r)
	if err != nil {
		http.Error(w, `{"error":"missing source name"}`, http.StatusBadRequest)
		return
	}
	page := parsePage(r.URL.Query().Get("page"))
	const pageSize int64 = 25
	offset := (page - 1) * pageSize

	items, err := h.rateService.ObtainDailyEventSummaryBySource(r.Context(), name, offset, pageSize)
	if err != nil {
		h.internalError(w, err)
		return
	}
	resp := make([]dto.DailyEventResponse, 0, len(items))
	for _, s := range items {
		resp = append(resp, dto.DailyEventResponse{
			Type:         s.UserType,
			Date:         s.Date,
			SuccessCount: s.SuccessCount,
			FailedCount:  s.FailedCount,
		})
	}
	writeJSON(w, resp)
}

// ListExecutionErrors returns paginated failed execution history records from all sources.
//
// GET /api/errors/execution?page=N
func (h *Handler) ListExecutionErrors(w http.ResponseWriter, r *http.Request) {
	page := parsePage(r.URL.Query().Get("page"))
	const pageSize int64 = 50
	offset := (page - 1) * pageSize

	items, err := h.rateService.ObtainLastNExecutionHistoryErrors(r.Context(), offset, pageSize)
	if err != nil {
		h.internalError(w, err)
		return
	}
	resp := make([]dto.ExecutionErrorResponse, 0, len(items))
	for _, rec := range items {
		resp = append(resp, dto.ExecutionErrorResponse{
			ID:         rec.ID,
			SourceName: rec.SourceName,
			Error:      rec.Error,
			Timestamp:  rec.Timestamp.Format(time.RFC3339),
		})
	}
	writeJSON(w, resp)
}

// ListMeSubscriptions returns the caller's own subscriptions enriched with the
// latest rate value and timestamp per source.
//
// GET /api/me/subscriptions
// Auth: X-Telegram-Init-Data header. The previous ?initData= query-string
// fallback was removed because the HMAC-signed initData would otherwise land in
// access logs and Referer headers for up to its 24h validity window.
func (h *Handler) ListMeSubscriptions(w http.ResponseWriter, r *http.Request) {
	initData := r.Header.Get("X-Telegram-Init-Data")

	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	q := r.URL.Query().Get("q")
	page := parsePage(r.URL.Query().Get("page"))
	pageSize, err := parsePageSize(r.URL.Query().Get("page_size"))
	if err != nil {
		http.Error(w, `{"error":"page_size must be a number"}`, http.StatusBadRequest)
		return
	}

	// TODO: DM-only assumption — this bot stores subscriptions keyed by Telegram chat_id,
	// which equals user_id for direct chats. If the bot is ever added to groups the
	// subscriptions keyed under group chat_ids will not appear here. See plan R5.
	tgUserID := strconv.FormatInt(userID, 10)
	subs, err := h.meSubRepo.ObtainRateUserSubscriptionsByUserID(r.Context(), domain.UserTypeTelegram, tgUserID)
	if err != nil {
		h.internalError(w, err)
		return
	}

	// Group subscriptions by source name, collecting conditions for the same source.
	type group struct {
		sourceName string
		conditions []string
	}
	seen := make(map[string]int) // sourceName → index in groups
	groups := make([]group, 0)
	for _, s := range subs {
		cond := string(s.ConditionType) + ":" + s.ConditionValue
		if idx, ok := seen[s.SourceName]; ok {
			groups[idx].conditions = append(groups[idx].conditions, cond)
		} else {
			seen[s.SourceName] = len(groups)
			groups = append(groups, group{sourceName: s.SourceName, conditions: []string{cond}})
		}
	}

	// Bulk-load every distinct source up front so the search and render loops
	// are O(1) per group instead of one ObtainRateSourceByName transaction each
	// (the previous 2*M N+1 pattern).
	sourceNames := make([]string, 0, len(groups))
	for _, g := range groups {
		sourceNames = append(sourceNames, g.sourceName)
	}
	sourceMap, err := h.meSourceRepo.ObtainRateSourcesByNames(r.Context(), sourceNames)
	if err != nil {
		h.internalError(w, err)
		return
	}

	// Apply case-insensitive substring search before pagination.
	var filtered []group
	if q == "" {
		filtered = groups
	} else {
		lq := strings.ToLower(q)
		for _, g := range groups {
			src, ok := sourceMap[g.sourceName]
			if !ok {
				continue
			}
			pair := strings.ToLower(src.BaseCurrency + "/" + src.QuoteCurrency)
			if strings.Contains(strings.ToLower(src.Title), lq) ||
				strings.Contains(strings.ToLower(src.Name), lq) ||
				strings.Contains(pair, lq) {
				filtered = append(filtered, g)
			}
		}
	}

	total := int64(len(filtered))

	// Paginate.
	offset := (page - 1) * pageSize
	if offset >= total {
		offset = max(total, 0)
	}
	end := offset + pageSize
	if end > total {
		end = total
	}
	pageItems := filtered[offset:end]

	// Bulk-load the latest rate value per page item so the render loop is O(1)
	// per row. Previously one ObtainLastNRateValuesBySourceName transaction per
	// page item — pageSize=50 → 50 round-trips per request.
	rateNames := make([]string, 0, len(pageItems))
	for _, g := range pageItems {
		rateNames = append(rateNames, g.sourceName)
	}
	latestRates, err := h.meRateValueRepo.ObtainLatestRateValuesBySourceNames(r.Context(), rateNames)
	if err != nil {
		h.internalError(w, err)
		return
	}

	items := make([]dto.MeSubscriptionRow, 0, len(pageItems))
	for _, g := range pageItems {
		row := dto.MeSubscriptionRow{
			SourceName: g.sourceName,
			Conditions: g.conditions,
		}
		if src, ok := sourceMap[g.sourceName]; ok {
			row.SourceTitle = src.Title
			row.BaseCurrency = src.BaseCurrency
			row.QuoteCurrency = src.QuoteCurrency
		}
		if rv, ok := latestRates[g.sourceName]; ok {
			row.LatestPrice = rv.Price
			row.LatestAt = rv.Timestamp.UTC().Format(time.RFC3339)
		}
		items = append(items, row)
	}

	writeJSON(w, dto.MeSubscriptionsResponse{
		Items:    items,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	})
}

// ListMeSubscriptionsRaw returns the caller's own subscriptions as one row per
// condition, each carrying its stable subscription ID. Unlike ListMeSubscriptions
// (which groups a source's conditions into one enriched row), this exposes the
// raw per-condition granularity the editor screen needs. Items are sorted
// source_name ASC, updated_at DESC so the editor groups rows by source
// without additional client-side work.
//
// GET /api/me/subscriptions/raw
// Auth: X-Telegram-Init-Data header (same HMAC scheme as ListMeSubscriptions).
func (h *Handler) ListMeSubscriptionsRaw(w http.ResponseWriter, r *http.Request) {
	initData := r.Header.Get("X-Telegram-Init-Data")
	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	tgUserID := strconv.FormatInt(userID, 10)
	subs, err := h.meSubRepo.ObtainRateUserSubscriptionsByUserID(r.Context(), domain.UserTypeTelegram, tgUserID)
	if err != nil {
		h.internalError(w, err)
		return
	}

	// Collect distinct source names for a bulk metadata load (avoids N+1).
	seen := make(map[string]struct{}, len(subs))
	sourceNames := make([]string, 0, len(subs))
	for _, s := range subs {
		if _, ok := seen[s.SourceName]; !ok {
			seen[s.SourceName] = struct{}{}
			sourceNames = append(sourceNames, s.SourceName)
		}
	}
	sourceMap, err := h.meSourceRepo.ObtainRateSourcesByNames(r.Context(), sourceNames)
	if err != nil {
		h.internalError(w, err)
		return
	}

	// Sort: source_name ASC, updated_at DESC.
	sort.Slice(subs, func(i, j int) bool {
		if subs[i].SourceName != subs[j].SourceName {
			return subs[i].SourceName < subs[j].SourceName
		}
		return subs[i].UpdatedAt.After(subs[j].UpdatedAt)
	})

	items := make([]dto.MeSubscriptionEditRow, 0, len(subs))
	for _, s := range subs {
		row := dto.MeSubscriptionEditRow{
			ID:             s.ID,
			SourceName:     s.SourceName,
			ConditionType:  string(s.ConditionType),
			ConditionValue: s.ConditionValue,
			UpdatedAt:      s.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if src, ok := sourceMap[s.SourceName]; ok {
			row.SourceTitle = src.Title
			row.BaseCurrency = src.BaseCurrency
			row.QuoteCurrency = src.QuoteCurrency
		}
		items = append(items, row)
	}

	writeJSON(w, dto.MeSubscriptionsRawResponse{Items: items})
}

// CreateMeSubscription creates a new subscription owned by the authenticated caller.
//
// POST /api/me/subscriptions
// Auth: X-Telegram-Init-Data header (same HMAC scheme as ListMeSubscriptions).
// Body: {"source_name":"...", "condition_type":"...", "condition_value":"..."}
//
// 201 Created with {"id":"<generated>"} on success.
// 400 on malformed body, unknown source, or invalid condition.
// 401 on missing/invalid initData.
// 500 on persistence failure.
func (h *Handler) CreateMeSubscription(w http.ResponseWriter, r *http.Request) {
	initData := r.Header.Get("X-Telegram-Init-Data")
	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	chatIDStr := strconv.FormatInt(userID, 10)

	r.Body = http.MaxBytesReader(w, r.Body, 4<<10) // 4 KiB
	var body dto.MeSubscriptionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	// Guard against FK failure: verify the source exists before inserting.
	src, err := h.meSourceRepo.ObtainRateSourceByName(r.Context(), body.SourceName)
	if err != nil {
		h.internalError(w, fmt.Errorf("CreateMeSubscription source lookup: %w", err))
		return
	}
	if src == nil {
		pub := internal.NewPublicError("unknown source")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}

	record := domain.RateUserSubscription{
		UserType:       domain.UserTypeTelegram,
		UserID:         chatIDStr,
		SourceName:     body.SourceName,
		ConditionType:  domain.SubscriptionConditionType(body.ConditionType),
		ConditionValue: body.ConditionValue,
	}
	if err := record.Validate(); err != nil {
		pub := internal.NewPublicError(fmt.Sprintf("invalid condition value for %s: check the format and try again", record.ConditionType))
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}

	if err := h.meSubRepo.RetainRateUserSubscription(r.Context(), &record); err != nil {
		h.internalError(w, fmt.Errorf("CreateMeSubscription retain: %w", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(dto.MeSubscriptionCreateResponse{ID: record.ID}); err != nil {
		h.logger.Print(errors.Join(
			fmt.Errorf("encode CreateMeSubscription response: %w", err),
			internal.NewTraceError(),
		))
	}
}

// meSubscriptionOwnershipCheck loads the subscription by id, verifies the
// caller owns it, and returns it. On not-found or ownership mismatch it writes
// a 404 and returns nil. On repo error it writes 500 and returns nil.
// Callers must return when this function returns nil.
func (h *Handler) meSubscriptionOwnershipCheck(w http.ResponseWriter, r *http.Request, id, chatIDStr string) *domain.RateUserSubscription {
	sub, err := h.meSubRepo.ObtainRateUserSubscriptionByID(r.Context(), id)
	if err != nil {
		h.internalError(w, fmt.Errorf("subscription lookup: %w", err))
		return nil
	}
	if sub == nil || sub.UserID != chatIDStr {
		// 404 (not 403) to avoid disclosing another user's subscription. "No
		// such row" and "wrong owner" share the same PublicError message so the
		// distinction is invisible externally.
		pub := internal.NewPublicError("subscription not found")
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusNotFound)
		return nil
	}
	return sub
}

// UpdateMeSubscription updates the condition fields of an existing subscription
// owned by the authenticated caller.
//
// PATCH /api/me/subscriptions/{id}
// Auth: X-Telegram-Init-Data header.
// Body: {"condition_type":"...", "condition_value":"..."}
//
// 204 No Content on success.
// 400 on malformed body or invalid condition.
// 401 on auth failure.
// 404 on missing subscription or cross-user access (same response — no existence disclosure).
// 500 on persistence failure.
func (h *Handler) UpdateMeSubscription(w http.ResponseWriter, r *http.Request) {
	initData := r.Header.Get("X-Telegram-Init-Data")
	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	chatIDStr := strconv.FormatInt(userID, 10)

	// Cap the body before any reads — including the ownership DB query — so an
	// authenticated owner cannot hold the connection open with a large body
	// during the lookup window.
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10) // 4 KiB

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing subscription id"}`, http.StatusBadRequest)
		return
	}

	sub := h.meSubscriptionOwnershipCheck(w, r, id, chatIDStr)
	if sub == nil {
		return
	}
	var body dto.MeSubscriptionUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	sub.ConditionType = domain.SubscriptionConditionType(body.ConditionType)
	sub.ConditionValue = body.ConditionValue
	if err := sub.Validate(); err != nil {
		pub := internal.NewPublicError(fmt.Sprintf("invalid condition value for %s: check the format and try again", sub.ConditionType))
		http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
		return
	}

	if err := h.meSubRepo.RetainRateUserSubscription(r.Context(), sub); err != nil {
		h.internalError(w, fmt.Errorf("UpdateMeSubscription retain: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteMeSubscription removes a subscription owned by the authenticated caller.
//
// DELETE /api/me/subscriptions/{id}
// Auth: X-Telegram-Init-Data header.
//
// 204 No Content on success.
// 401 on auth failure.
// 404 on missing subscription or cross-user access.
// 500 on persistence failure.
//
// Deleting a subscription does NOT remove rate_user_events rows for that
// user/source — events are FK'd to rate_sources, not to individual
// subscription rows, so they are treated as historical truth and left intact.
func (h *Handler) DeleteMeSubscription(w http.ResponseWriter, r *http.Request) {
	initData := r.Header.Get("X-Telegram-Init-Data")
	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	chatIDStr := strconv.FormatInt(userID, 10)

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing subscription id"}`, http.StatusBadRequest)
		return
	}

	sub := h.meSubscriptionOwnershipCheck(w, r, id, chatIDStr)
	if sub == nil {
		return
	}

	if err := h.meSubRepo.RemoveRateUserSubscription(r.Context(), sub); err != nil {
		h.internalError(w, fmt.Errorf("DeleteMeSubscription remove: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpsertMeProfile stores the caller's IANA timezone so notification timestamps
// render in their local time. Fire-and-forget from the Mini App on every mount:
// the client sends whatever Intl.DateTimeFormat resolves to; the server
// validates via time.LoadLocation.
//
// POST /api/me/profile
// Body: {"timezone":"Asia/Almaty"}
// Auth: X-Telegram-Init-Data header (same HMAC scheme as ListMeSubscriptions).
//
// 204 No Content on success, 400 on bad timezone, 401 on auth failure, 500
// on persistence failure. The response body is empty on success — Mini App
// callers fire-and-forget and discard it.
func (h *Handler) UpsertMeProfile(w http.ResponseWriter, r *http.Request) {
	initData := r.Header.Get("X-Telegram-Init-Data")
	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// Bound the read so a malicious or buggy client cannot inflate memory; the
	// body should be a tiny JSON object.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10) // 1 KiB
	var body dto.MeProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	body.Timezone = strings.TrimSpace(body.Timezone)
	body.Locale = strings.TrimSpace(body.Locale)
	if body.Timezone == "" {
		http.Error(w, `{"error":"timezone is required"}`, http.StatusBadRequest)
		return
	}
	// Bound locale so a buggy caller can't dump megabytes into the column.
	// BCP-47 tags max out around 35 chars in practice; 64 is a safe cap that
	// won't reject any realistic value.
	if len(body.Locale) > 64 {
		http.Error(w, `{"error":"locale too long"}`, http.StatusBadRequest)
		return
	}

	tgUserID := strconv.FormatInt(userID, 10)
	record := &domain.RateUserProfile{
		UserType: domain.UserTypeTelegram,
		UserID:   tgUserID,
		Timezone: body.Timezone,
		Locale:   body.Locale,
	}
	if err := h.meProfileRepo.UpsertRateUserProfile(r.Context(), record); err != nil {
		// PublicError → 400 with the safe message. Other errors → 500.
		var pub *internal.PublicError
		if errors.As(err, &pub) {
			http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
			return
		}
		h.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetMeRatesChart returns the sparkline-list chart data for the calling user's
// subscribed currency pairs. The window is the optional ?period= query parameter
// (one of 7, 30, 90, 180, 360; default 7). BID and ASK for the same canonical
// pair appear as a single row with two series entries.
//
// GET /api/me/rates/chart?period=N
// Auth: X-Telegram-Init-Data header only. The HMAC-signed payload must never be
// passed via query string (it would appear in access logs and Referer headers).
//
// The pair display label is always BID-natural (e.g. "USD/KZT") regardless of
// subscribed directions; the service layer owns label assignment, not this handler.
//
// Returns 400 with a PublicError body when period is present but not in the
// whitelist {7, 30, 90, 180, 360}.
func (h *Handler) GetMeRatesChart(w http.ResponseWriter, r *http.Request) {
	initData := r.Header.Get("X-Telegram-Init-Data")

	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	if h.meChartSvc == nil {
		http.Error(w, `{"error":"chart service unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	periodDays, err := parseChartPeriod(r.URL.Query().Get("period"))
	if err != nil {
		var pub *internal.PublicError
		if errors.As(err, &pub) {
			http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
			return
		}
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}

	tgUserID := strconv.FormatInt(userID, 10)
	ch, err := h.meChartSvc.ObtainMeChartForPeriod(r.Context(), tgUserID, periodDays)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// Client navigated away or the request timed out — a normal
			// client-side event, not a server failure. 499 ("client closed
			// request") distinguishes it in access logs from genuine 500s.
			http.Error(w, `{"error":"request cancelled"}`, 499)
			return
		}
		h.internalError(w, fmt.Errorf("GetMeRatesChart: %w", err))
		return
	}

	pairRows := make([]dto.MeChartPairRow, 0, len(ch.Pairs))
	for _, row := range ch.Pairs {
		seriesDTOs := make([]dto.MeChartSeries, 0, len(row.Series))
		for _, sr := range row.Series {
			s := dto.MeChartSeries{
				Kind:          string(sr.Kind),
				Color:         sr.Color,
				Latest:        sr.Latest,
				DeltaPct:      sr.DeltaPct,
				Sparse:        sr.Sparse,
				EffectiveDays: sr.EffectiveDays,
			}
			if len(sr.Points) > 0 {
				pts := make([]dto.MeChartPoint, 0, len(sr.Points))
				for _, p := range sr.Points {
					pts = append(pts, dto.MeChartPoint{
						Timestamp: p.Timestamp,
						Value:     p.Value,
					})
				}
				s.Points = pts
			}
			seriesDTOs = append(seriesDTOs, s)
		}
		pairRows = append(pairRows, dto.MeChartPairRow{
			Pair:      row.Pair,
			Category:  string(row.Category),
			SpreadPct: row.SpreadPct,
			Series:    seriesDTOs,
		})
	}

	writeJSON(w, dto.MeChartResponse{
		Window: fmt.Sprintf("%d days", periodDays),
		Pairs:  pairRows,
	})
}

// GetMeRatesHistory returns paginated rate-collection events for the calling
// user's subscribed sources matching the given canonical pair label.
//
// GET /api/me/rates/history?pair=<canonical>&page=<n>&limit=<n>&source_title=<title>
// Auth: X-Telegram-Init-Data header only.
//
// source_title is an optional exact-match filter; when present, Total reflects
// the filtered grouped count. An unknown source_title (not matching any provider
// title in the user's subscriptions for this pair) returns 200 with empty Items
// and Total=0, not 400.
//
//   - 400 on missing or empty pair.
//   - 400 on non-integer limit.
//   - 401 on bad initData.
//   - 499 on ctx canceled / deadline exceeded.
//   - 200 with an empty Items list when the user has no matching
//     subscriptions (NOT 404).
func (h *Handler) GetMeRatesHistory(w http.ResponseWriter, r *http.Request) {
	initData := r.Header.Get("X-Telegram-Init-Data")

	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	if h.meChartSvc == nil {
		http.Error(w, `{"error":"chart service unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	pair := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("pair")))
	if pair == "" {
		http.Error(w, `{"error":"pair is required"}`, http.StatusBadRequest)
		return
	}

	sourceTitle := strings.TrimSpace(r.URL.Query().Get("source_title"))

	page := parsePage(r.URL.Query().Get("page"))
	limit, err := parseHistoryLimit(r.URL.Query().Get("limit"))
	if err != nil {
		http.Error(w, `{"error":"limit must be a number"}`, http.StatusBadRequest)
		return
	}

	tgUserID := strconv.FormatInt(userID, 10)
	result, err := h.meChartSvc.ObtainMeHistory(r.Context(), tgUserID, pair, sourceTitle, page, limit)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, `{"error":"request cancelled"}`, 499)
			return
		}
		h.internalError(w, fmt.Errorf("GetMeRatesHistory: %w", err))
		return
	}

	items := make([]dto.MeHistoryRow, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, dto.MeHistoryRow{
			SourceTitle: row.SourceTitle,
			Timestamp:   row.Timestamp,
			Bid:         row.Bid,
			Ask:         row.Ask,
			BidDeltaPct: row.BidDeltaPct,
			AskDeltaPct: row.AskDeltaPct,
		})
	}

	writeJSON(w, dto.MeHistoryResponse{
		Pair:  result.Pair,
		Page:  int(page),
		Limit: int(limit),
		Total: result.Total,
		Items: items,
	})
}

// GetPublicRatesChart returns the paginated sparkline-list chart for every
// distinct active (base, quote, kind) triple in the system. The window is the
// optional ?period= query parameter (one of 7, 30, 90, 180, 360; default 7).
// No auth required.
//
// GET /api/public/rates/chart?page=N&limit=L&period=P
//
//   - 400 on non-integer limit.
//   - 400 on period present but not in whitelist {7, 30, 90, 180, 360}.
//   - 499 on ctx canceled / deadline exceeded.
//   - 500 on service-layer failures.
func (h *Handler) GetPublicRatesChart(w http.ResponseWriter, r *http.Request) {
	if h.meChartSvc == nil {
		http.Error(w, `{"error":"chart service unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	page := parsePage(r.URL.Query().Get("page"))
	limit, err := parsePublicChartLimit(r.URL.Query().Get("limit"))
	if err != nil {
		publicErr := internal.NewPublicError("limit must be a number")
		http.Error(w, `{"error":"`+publicErr.Details()+`"}`, http.StatusBadRequest)
		return
	}

	periodDays, err := parseChartPeriod(r.URL.Query().Get("period"))
	if err != nil {
		var pub *internal.PublicError
		if errors.As(err, &pub) {
			http.Error(w, `{"error":"`+pub.Details()+`"}`, http.StatusBadRequest)
			return
		}
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}

	ch, total, err := h.meChartSvc.ObtainPublicChartForPeriod(r.Context(), page, limit, periodDays)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, `{"error":"request cancelled"}`, 499)
			return
		}
		h.internalError(w, fmt.Errorf("GetPublicRatesChart: %w", err))
		return
	}

	pairRows := make([]dto.MeChartPairRow, 0, len(ch.Pairs))
	for _, row := range ch.Pairs {
		seriesDTOs := make([]dto.MeChartSeries, 0, len(row.Series))
		for _, sr := range row.Series {
			s := dto.MeChartSeries{
				Kind:          string(sr.Kind),
				Color:         sr.Color,
				Latest:        sr.Latest,
				DeltaPct:      sr.DeltaPct,
				Sparse:        sr.Sparse,
				EffectiveDays: sr.EffectiveDays,
			}
			if len(sr.Points) > 0 {
				pts := make([]dto.MeChartPoint, 0, len(sr.Points))
				for _, p := range sr.Points {
					pts = append(pts, dto.MeChartPoint{
						Timestamp: p.Timestamp,
						Value:     p.Value,
					})
				}
				s.Points = pts
			}
			seriesDTOs = append(seriesDTOs, s)
		}
		pairRows = append(pairRows, dto.MeChartPairRow{
			Pair:      row.Pair,
			Category:  string(row.Category),
			SpreadPct: row.SpreadPct,
			Series:    seriesDTOs,
		})
	}

	writeJSON(w, dto.PublicChartResponse{
		Window: fmt.Sprintf("%d days", periodDays),
		Page:   int(page),
		Limit:  int(limit),
		Total:  total,
		Pairs:  pairRows,
	})
}

// internalError logs the underlying error with a trace and returns a generic 500 to the client.
func (h *Handler) internalError(w http.ResponseWriter, err error) {
	h.logger.Print(errors.Join(err, internal.NewTraceError()))
	http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
}

const (
	meSubscriptionsMaxAge      = 24 * time.Hour
	meSubscriptionsDefaultPage = int64(1)
	meSubscriptionsDefaultSize = int64(10)
	meSubscriptionsMaxSize     = int64(50)
)

// writeJSON sets Content-Type and encodes v as JSON.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Print(errors.Join(
			fmt.Errorf("encode response body: %w", err),
			internal.NewTraceError(),
		))
	}
}

// parsePageMax caps the ?page= query parameter. Picked well above any
// realistic dataset (1 << 30 ≈ 10^9 pages); paired with a 100-item limit it
// keeps offset arithmetic strictly inside int64.
const parsePageMax = int64(1) << 30

// parsePage parses a "page" query parameter, defaulting to 1 when missing,
// malformed, or non-positive. Values above parsePageMax are clamped so the
// downstream offset arithmetic (offset = (page - 1) * limit) cannot overflow
// int64 into a negative OFFSET — which SQLite treats as no limit and fans into
// a full table scan. Malformed values fall through silently because public
// endpoints would otherwise generate unbounded log noise from fuzzed traffic.
func parsePage(raw string) int64 {
	if raw == "" {
		return 1
	}
	page, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || page < 1 {
		return 1
	}
	if page > parsePageMax {
		return parsePageMax
	}
	return page
}

// extractLimit reads the ?limit= query parameter, clamped to [10, 100], default 50.
func extractLimit(uri *url.URL) (int64, error) {
	var result int64 = 50

	if v := uri.Query().Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, errors.Join(err, internal.NewTraceError())
		}
		if n > 0 {
			result = n
		}
	}

	result = min(result, 100)
	result = max(result, 10)

	return result, nil
}

// extractOffset reads the ?offset= query parameter. Returns 0 when absent.
func extractOffset(r *http.Request) (int64, error) {
	var result int64
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, errors.Join(err, internal.NewTraceError())
		}
		if n > 0 {
			result = n
		}
	}
	return max(result, 0), nil
}

// extractName reads the {name} path segment set by Go 1.22's ServeMux.
// Returns an error when the segment is absent so callers can return 400.
func extractName(r *http.Request) (string, error) {
	v := r.PathValue("name")
	if v == "" {
		return "", fmt.Errorf("missing path param: name")
	}
	return v, nil
}

const (
	meHistoryDefaultLimit = int64(20)
	meHistoryMaxLimit     = int64(100)
)

// parseHistoryLimit parses the ?limit= query parameter for the history
// endpoint, clamped to [1, meHistoryMaxLimit], default meHistoryDefaultLimit.
// Returns an error only when the value is present but non-integer.
func parseHistoryLimit(raw string) (int64, error) {
	if raw == "" {
		return meHistoryDefaultLimit, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 1 {
		n = meHistoryDefaultLimit
	}
	if n > meHistoryMaxLimit {
		n = meHistoryMaxLimit
	}
	return n, nil
}

const (
	publicChartDefaultLimit = int64(20)
	publicChartMaxLimit     = int64(100)
)

// parsePublicChartLimit parses the ?limit= query parameter for the public chart
// endpoint. Default 20; values < 1 clamp to 20, values > 100 clamp to 100.
// Returns an error only when the value is present but non-integer, matching
// parseHistoryLimit.
func parsePublicChartLimit(raw string) (int64, error) {
	if raw == "" {
		return publicChartDefaultLimit, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 1 {
		n = publicChartDefaultLimit
	}
	if n > publicChartMaxLimit {
		n = publicChartMaxLimit
	}
	return n, nil
}

// allowedChartPeriods is the whitelist of accepted period values for the chart
// endpoints. Only these exact integers are valid; anything else returns 400.
var allowedChartPeriods = []int64{7, 30, 90, 180, 360}

// parseChartPeriod parses the raw ?period= query value. An empty string returns
// the default 7. Any non-empty value not in {7, 30, 90, 180, 360} returns a
// PublicError so the handler can surface it inline to the client.
func parseChartPeriod(raw string) (int64, error) {
	if raw == "" {
		return 7, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, internal.NewPublicError("period must be one of 7, 30, 90, 180, 360")
	}
	for _, allowed := range allowedChartPeriods {
		if n == allowed {
			return n, nil
		}
	}
	return 0, internal.NewPublicError("period must be one of 7, 30, 90, 180, 360")
}

// parsePageSize parses a "page_size" query parameter, clamped to [1, 50], default 10.
func parsePageSize(raw string) (int64, error) {
	if raw == "" {
		return meSubscriptionsDefaultSize, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 1 {
		n = meSubscriptionsDefaultSize
	}
	if n > meSubscriptionsMaxSize {
		n = meSubscriptionsMaxSize
	}
	return n, nil
}
