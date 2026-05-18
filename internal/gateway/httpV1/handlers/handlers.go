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
	"strconv"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/application/rulegen"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/repository"
	"github.com/seilbekskindirov/monitor/internal/tools/tgwebapp"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

// NewHandler constructs a Handler wired to the rate service and, optionally,
// to the Mini App auth dependencies. botToken, meSubRepo, meSourceRepo, and
// meRateValueRepo are required for ListMeSubscriptions; the remaining handlers
// only need srvRate. defaultGenerator, generatorFactory, adminChatID, and
// lockMgr are required for GenerateRules; pass nil/nil/0/nil in tests that do
// not exercise it.
func NewHandler(
	srvRate rateService,
	botToken string,
	meSubRepo meSubscriptionRepository,
	meSourceRepo meSourceRepository,
	meRateValueRepo meRateValueRepository,
	defaultGenerator rulegenGenerator,
	generatorFactory rulegenGeneratorFactory,
	adminChatID int64,
	lockMgr *rulegen.LockManager,
) (*Handler, error) {
	h := &Handler{
		rateService:      srvRate,
		botToken:         botToken,
		meSubRepo:        meSubRepo,
		meSourceRepo:     meSourceRepo,
		meRateValueRepo:  meRateValueRepo,
		validateInitData: tgwebapp.ValidateInitData,
		nowFn:            time.Now,
		defaultGenerator: defaultGenerator,
		generatorFactory: generatorFactory,
		adminChatID:      adminChatID,
		lockMgr:          lockMgr,
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

	// validateInitData is the Telegram WebApp initData verifier. It is a field so
	// tests can inject a fake without needing real bot tokens.
	validateInitData func(initData, botToken string, maxAge time.Duration, now time.Time) (int64, error)
	// nowFn returns the current time. Injected for deterministic tests.
	nowFn func() time.Time

	// rulegen dependencies — required for GenerateRules.
	defaultGenerator rulegenGenerator
	generatorFactory rulegenGeneratorFactory
	adminChatID      int64
	lockMgr          *rulegen.LockManager
}

// internalError logs the underlying error with a trace and returns a generic 500 to the client.
func (h *Handler) internalError(w http.ResponseWriter, err error) {
	log.Print(errors.Join(err, internal.NewTraceError()))
	http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
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

	resp := make([]api.SourceResponse, 0, len(sources))
	for _, s := range sources {
		item := api.SourceResponse{
			Name:          s.Name,
			Title:         s.Title,
			BaseCurrency:  s.BaseCurrency,
			QuoteCurrency: s.QuoteCurrency,
			Interval:      s.Interval,
			Active:        s.Active,
		}
		recs, recsErr := h.rateService.ObtainLastNExecutionHistoryBySourceName(r.Context(), s.Name, 1)
		if recsErr != nil {
			log.Print(errors.Join(
				fmt.Errorf("list last execution for %q: %w", s.Name, recsErr),
				internal.NewTraceError(),
			))
		} else if len(recs) > 0 {
			item.LastSuccess = recs[0].Success
			item.LastError = recs[0].Error
			item.LastRunAt = recs[0].Timestamp.Format(time.RFC3339)
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

	resp := make([]api.RateResponse, 0, len(rates))
	for _, rv := range rates {
		resp = append(resp, api.RateResponse{
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

	resp := make([]api.HistoryResponse, 0, len(recs))
	for _, rec := range recs {
		resp = append(resp, api.HistoryResponse{
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

	resp := make([]api.NotificationResponse, 0, len(records))
	for _, rec := range records {
		resp = append(resp, api.NotificationResponse{
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

	resp := make([]api.NotificationResponse, 0, len(records))
	for _, rec := range records {
		resp = append(resp, api.NotificationResponse{
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
	resp := make([]api.NotificationResponse, 0, len(events))
	for _, e := range events {
		resp = append(resp, api.NotificationResponse{
			ID:        e.ID,
			UserType:  string(e.UserType),
			Status:    string(e.Status),
			CreatedAt: e.CreatedAt,
		})
	}
	writeJSON(w, resp)
}

// GetRatesChart returns aggregated rate data for a source over a given period.
//
// GET /api/sources/{name}/rates/chart?period=week|month|year
func (h *Handler) GetRatesChart(w http.ResponseWriter, r *http.Request) {
	name, err := extractName(r)
	if err != nil {
		http.Error(w, `{"error":"missing source name"}`, http.StatusBadRequest)
		return
	}
	period := repository.ChartPeriod(r.URL.Query().Get("period"))
	if period == "" {
		period = repository.ChartPeriodWeek
	}
	points, err := h.rateService.ObtainRateValueChartBySourceName(r.Context(), name, period)
	if err != nil {
		h.internalError(w, err)
		return
	}
	resp := make([]api.ChartPointResponse, 0, len(points))
	for _, p := range points {
		resp = append(resp, api.ChartPointResponse{Label: p.Label, Price: p.Price})
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
	resp := make([]api.NotificationResponse, 0, len(events))
	for _, e := range events {
		resp = append(resp, api.NotificationResponse{
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
	resp := make([]api.SubscriptionSummaryResponse, 0, len(summaries))
	for _, s := range summaries {
		item := api.SubscriptionSummaryResponse{
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

	var body api.SourceActiveRequest
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
	writeJSON(w, api.StatsResponse{
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
	resp := make([]api.SubscriptionDetailResponse, 0, len(items))
	for _, s := range items {
		item := api.SubscriptionDetailResponse{
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
	resp := make([]api.DailyEventResponse, 0, len(items))
	for _, s := range items {
		resp = append(resp, api.DailyEventResponse{
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
	resp := make([]api.ExecutionErrorResponse, 0, len(items))
	for _, rec := range items {
		resp = append(resp, api.ExecutionErrorResponse{
			ID:         rec.ID,
			SourceName: rec.SourceName,
			Error:      rec.Error,
			Timestamp:  rec.Timestamp.Format(time.RFC3339),
		})
	}
	writeJSON(w, resp)
}

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

// parsePage parses a "page" query string parameter, defaulting to 1 when
// the value is missing, malformed, or non-positive. Parse failures are
// logged so misconfigured callers can be diagnosed without surfacing 4xx.
func parsePage(raw string) int64 {
	if raw == "" {
		return 1
	}
	page, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Print(errors.Join(
			fmt.Errorf("parse page %q: %w", raw, err),
			internal.NewTraceError(),
		))
		return 1
	}
	if page < 1 {
		return 1
	}
	return page
}

type rateService interface {
	ObtainLastNExecutionHistoryBySourceName(ctx context.Context, name string, limit int64) ([]domain.ExecutionHistory, error)
	ObtainLastSuccessNExecutionHistoryBySourceName(ctx context.Context, name string, limit int64) ([]domain.ExecutionHistory, error)
	ObtainAllRateSources(ctx context.Context) ([]domain.RateSource, error)
	UpdateRateSourceActive(ctx context.Context, name string, active bool) error
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, limit int64) ([]domain.RateValue, error)
	ObtainListOfLastRateUserEvent(ctx context.Context, limit int64) ([]domain.RateUserEvent, error)
	ObtainFailedListOfRateUserEvent(ctx context.Context, offset, limit int64) ([]domain.RateUserEvent, error)
	ObtainPendingRateUserEvents(ctx context.Context) ([]domain.RateUserEvent, error)
	ObtainRateValueChartBySourceName(ctx context.Context, name string, period repository.ChartPeriod) ([]repository.ChartPoint, error)
	ObtainFailedRateUserEventsBySourceName(ctx context.Context, sourceName string, page, pageSize int64) ([]domain.RateUserEvent, error)
	ObtainSubscriptionSummaryBySource(ctx context.Context, sourceName string) ([]domain.RateUserSubscriptionSummary, error)
	ObtainStats(ctx context.Context) (repository.StatsResult, error)
	ObtainRateUserSubscriptionsBySourcePaged(ctx context.Context, sourceName string, offset, limit int64) ([]domain.RateUserSubscriptionDetail, error)
	ObtainDailyEventSummaryBySource(ctx context.Context, sourceName string, offset, limit int64) ([]domain.RateUserEventDailySummary, error)
	ObtainLastNExecutionHistoryErrors(ctx context.Context, offset, limit int64) ([]domain.ExecutionHistory, error)
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
	meSubscriptionsMaxAge      = 24 * time.Hour
	meSubscriptionsDefaultPage = int64(1)
	meSubscriptionsDefaultSize = int64(10)
	meSubscriptionsMaxSize     = int64(50)
)

// ListMeSubscriptions returns the caller's own subscriptions enriched with the
// latest rate value and timestamp per source.
//
// GET /api/me/subscriptions
// Auth: X-Telegram-Init-Data header, or ?initData= query string as fallback.
func (h *Handler) ListMeSubscriptions(w http.ResponseWriter, r *http.Request) {
	initData := r.Header.Get("X-Telegram-Init-Data")
	if initData == "" {
		initData = r.URL.Query().Get("initData")
	}

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

	// Apply case-insensitive substring search before pagination.
	var filtered []group
	if q == "" {
		filtered = groups
	} else {
		lq := strings.ToLower(q)
		for _, g := range groups {
			src, srcErr := h.meSourceRepo.ObtainRateSourceByName(r.Context(), g.sourceName)
			if srcErr != nil || src == nil {
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
	page_items := filtered[offset:end]

	items := make([]api.MeSubscriptionRow, 0, len(page_items))
	for _, g := range page_items {
		src, srcErr := h.meSourceRepo.ObtainRateSourceByName(r.Context(), g.sourceName)
		if srcErr != nil {
			h.internalError(w, srcErr)
			return
		}
		row := api.MeSubscriptionRow{
			SourceName: g.sourceName,
			Conditions: g.conditions,
		}
		if src != nil {
			row.SourceTitle = src.Title
			row.BaseCurrency = src.BaseCurrency
			row.QuoteCurrency = src.QuoteCurrency
		}
		rates, ratesErr := h.meRateValueRepo.ObtainLastNRateValuesBySourceName(r.Context(), g.sourceName, 1)
		if ratesErr != nil {
			h.internalError(w, ratesErr)
			return
		}
		if len(rates) > 0 {
			row.LatestPrice = rates[0].Price
			row.LatestAt = rates[0].Timestamp.UTC().Format(time.RFC3339)
		}
		items = append(items, row)
	}

	writeJSON(w, api.MeSubscriptionsResponse{
		Items:    items,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	})
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

type meSubscriptionRepository interface {
	ObtainRateUserSubscriptionsByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error)
}

type meSourceRepository interface {
	ObtainRateSourceByName(ctx context.Context, name string) (*domain.RateSource, error)
}

type meRateValueRepository interface {
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, limit int64) ([]domain.RateValue, error)
}
