package notification

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// NewRateCheckAgent constructs a RateCheckAgent. All repository arguments are required
// except rRateUserProfile, which is optional — when nil, every notification renders in
// UTC. The optional shape lets tests that only exercise the alert pipeline skip the fake.
func NewRateCheckAgent(
	rRateSource rateSourceRepository,
	rRateValue rateValueRepository,
	rRateUserSubscription rateUserSubscriptionRepository,
	rRateUserEvent rateCheckEventRepository,
	rRateUserProfile rateUserProfileRepository,
	logger io.Writer,
) (*RateCheckAgent, error) {
	if rRateSource == nil || rRateValue == nil || rRateUserSubscription == nil || rRateUserEvent == nil {
		return nil, errors.New("all repository arguments are required")
	}
	if logger == nil {
		logger = io.Discard
	}
	return &RateCheckAgent{
		rateSourceRepository:           rRateSource,
		rateValueRepository:            rRateValue,
		rateUserSubscriptionRepository: rRateUserSubscription,
		rateUserEventRepository:        rRateUserEvent,
		rateUserProfileRepository:      rRateUserProfile,
		logger:                         logger,
	}, nil
}

// RateCheckAgent reads all sources and subscriptions from the DB, evaluates which
// notifications are due, builds alert messages, and queues them as RateUserEvents.
// One-shot: runs to completion per invocation, decoupled from data collection.
//
// rateUserProfileRepository is nil-safe: when not injected, timestamps render in
// UTC; when injected, each user's notifications use their stored IANA timezone,
// with UTC fallback on lookup failure / unknown zone.
type RateCheckAgent struct {
	rateSourceRepository           rateSourceRepository
	rateValueRepository            rateValueRepository
	rateUserSubscriptionRepository rateUserSubscriptionRepository
	rateUserEventRepository        rateCheckEventRepository
	rateUserProfileRepository      rateUserProfileRepository
	logger                         io.Writer
}

type rateSourceRepository interface {
	ObtainAllRateSources(context.Context) ([]domain.RateSource, error)
}

type rateValueRepository interface {
	ObtainLastNRateValuesBySourceName(context.Context, string, int64) ([]domain.RateValue, error)
}

type rateUserSubscriptionRepository interface {
	ObtainRateUserSubscriptionsBySource(context.Context, string) ([]domain.RateUserSubscription, error)
	RetainRateUserSubscription(context.Context, *domain.RateUserSubscription) error
}

// rateCheckEventRepository is intentionally narrower than rateUserEventRepository
// used by RateDispatchAgent — each type declares only what it needs.
type rateCheckEventRepository interface {
	RetainRateUserEvent(context.Context, *domain.RateUserEvent) error
}

// rateUserProfileRepository looks up the per-user timezone preference.
// Implementations return (nil, internal.ErrNotFound) when no row exists — a
// normal absence, not an error; the agent treats it as "use UTC".
type rateUserProfileRepository interface {
	ObtainRateUserProfileByUserID(context.Context, domain.UserType, string) (*domain.RateUserProfile, error)
}

// Run iterates every rate source, checks active subscriptions, and queues alert events
// for any subscription whose notification condition is currently satisfied.
//
// Cross-source dedup: subscriptions on the same (base, quote, kind) key for the same
// user produce a single table row regardless of which source they come from. The
// displayed price is the extremum across sources (BID→MAX, ASK→MIN, LAST→MAX) and the
// delta always comes from the same source as the winning price. LAST→MAX is a
// deterministic tiebreak; revisit to "most recent observation" when a second feed for
// one ticker is added. Every fired subscription is still retained individually so its
// LatestNotifiedRate advances and it does not re-fire.
func (a *RateCheckAgent) Run(ctx context.Context) error {
	sources, err := a.rateSourceRepository.ObtainAllRateSources(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	if len(sources) == 0 {
		return nil
	}

	now := time.Now().UTC()
	errs := make(map[string]error, len(sources))

	// userBuckets is hoisted outside the source loop so buckets accumulate
	// across all sources before being flattened into alerts.
	// Structure: userID → dedupKey → *dedupBucket, where
	// dedupKey = base|quote|kind (source name intentionally excluded).
	userBuckets := make(map[string]map[string]*dedupBucket)

	for _, source := range sources {
		values, err := a.rateValueRepository.ObtainLastNRateValuesBySourceName(ctx, source.Name, 1)
		if err != nil {
			errs[source.Name] = errors.Join(errs[source.Name], errors.Join(err, loginjector.NewTraceError()))
			continue
		}
		if len(values) == 0 {
			continue
		}
		currentValue := values[0].Price

		subscriptions, err := a.rateUserSubscriptionRepository.ObtainRateUserSubscriptionsBySource(ctx, source.Name)
		if err != nil {
			errs[source.Name] = errors.Join(errs[source.Name], errors.Join(err, loginjector.NewTraceError()))
			continue
		}

		// Key omits source.Name so the same pair+kind at multiple sources folds
		// into one row per user.
		key := source.BaseCurrency + "|" + source.QuoteCurrency + "|" + string(source.Kind)

		for _, sub := range subscriptions {
			delta := currentValue - sub.LatestNotifiedRate

			ok, err := sub.IsDue(now, delta)
			if err != nil {
				errs[source.Name] = errors.Join(errs[source.Name], errors.Join(err, loginjector.NewTraceError()))
				continue
			}
			if !ok {
				continue
			}

			// Capture LatestNotifiedRate before advancing so foldIntoBuckets gets the
			// pre-retain baseline: delta = currentValue - originalRate.
			originalRate := sub.LatestNotifiedRate

			// Always retain every fired subscription so LatestNotifiedRate advances,
			// preventing re-fire on unchanged rates. Unconditional and separate from
			// the bucket-fold below so a sub collapsing into an existing bucket still advances.
			sub.LatestNotifiedRate = currentValue
			if retainErr := a.rateUserSubscriptionRepository.RetainRateUserSubscription(ctx, &sub); retainErr != nil {
				errs[source.Name] = errors.Join(errs[source.Name], errors.Join(retainErr, loginjector.NewTraceError()))
			}

			if sub.UserType != domain.UserTypeTelegram {
				errs[source.Name] = errors.Join(errs[source.Name], fmt.Errorf("unsupported user type: %s", sub.UserType))
				continue
			}

			buckets := userBuckets[sub.UserID]
			if buckets == nil {
				buckets = make(map[string]*dedupBucket)
				userBuckets[sub.UserID] = buckets
			}

			// Snapshot carries the original rate so foldIntoBuckets computes
			// delta = currentValue - originalRate.
			snap := SubscriptionSnapshot{
				Subscription: domain.RateUserSubscription{LatestNotifiedRate: originalRate},
				Source:       source,
				CurrentPrice: currentValue,
			}
			foldIntoBuckets(buckets, snap)

			// Accumulate triggers from all contributing sources regardless of which
			// holds the winning price. Kept in the agent because the digest path has none.
			b := buckets[key]
			b.triggers[sub.ConditionType] = collapseConditionValue(
				b.triggers[sub.ConditionType],
				sub.ConditionValue,
				sub.ConditionType,
			)
		}
	}

	// Flatten user buckets into telegramBotAlerts, outside the source loop so
	// cross-source extremum selection is complete before alerts are built.
	telegramBotAlerts := make(map[string][]alert, len(userBuckets))
	for userID, buckets := range userBuckets {
		for _, b := range buckets {
			b.a.Triggers = sortedTriggers(b.triggers)
			telegramBotAlerts[userID] = append(telegramBotAlerts[userID], b.a)
		}
	}

	combined := make([]error, 0, len(errs)+len(telegramBotAlerts))
	for k, e := range errs {
		if e != nil {
			combined = append(combined, fmt.Errorf("source %s: %w", k, e))
		}
	}

	var (
		totalChats     int
		totalQueued    int
		totalAttempted int
	)
	for chatID, alerts := range telegramBotAlerts {
		loc := a.resolveUserTimezone(ctx, domain.UserTypeTelegram, chatID)
		msgs, err := buildAlertMessage(now, loc, alerts...)
		if err != nil {
			// Returned via combined; cmd/notifier logs the joined error at run
			// completion. Don't log here too — duplicate noise.
			combined = append(combined, fmt.Errorf("build message for chat_id=%s: %w", chatID, err))
			continue
		}
		// Bind the event to a source only when delivery covers exactly one;
		// alerts spanning multiple sources leave SourceName empty (→ NULL via
		// sourceNameForDB) so /events/failed by source isn't mislabelled.
		var eventSourceName string
		if singleSource := singleSourceName(alerts); singleSource != "" {
			eventSourceName = singleSource
		}
		var failCount int
		for _, msg := range msgs {
			if retainErr := a.rateUserEventRepository.RetainRateUserEvent(ctx, &domain.RateUserEvent{
				UserType:   domain.UserTypeTelegram,
				UserID:     chatID,
				Message:    msg,
				SourceName: eventSourceName,
			}); retainErr != nil {
				failCount++
				combined = append(combined, fmt.Errorf("retain event chat_id=%s: %w", chatID, retainErr))
			}
		}
		queued := len(msgs) - failCount
		if queued < 0 {
			queued = 0 // defensive — failCount is bounded by len(msgs) but
			// the type system does not enforce it.
		}
		totalChats++
		totalQueued += queued
		totalAttempted += len(msgs)
	}
	// Always emit a marker so operators have proof-of-execution even on quiet
	// runs; per-chat detail lives in combined and in rate_user_events. Routed
	// through a.logger (io.Discard fallback) so tests that leave logger nil or
	// pass io.Discard don't pollute the global logger.
	logger := a.logger
	if logger == nil {
		logger = io.Discard
	}
	fmt.Fprintf(logger, "notification check: queued %d/%d events across %d chats\n",
		totalQueued, totalAttempted, totalChats)

	return errors.Join(combined...)
}

// resolveUserTimezone returns the time.Location stored for (userType, userID),
// or nil when no profile is configured or the stored name is unknown to the Go
// runtime. nil tells buildAlertMessage to fall back to UTC, keeping the pipeline
// working before the user has ever opened the Mini App. Failures are logged once
// each, never fatal — a wrong timezone beats a missed notification.
func (a *RateCheckAgent) resolveUserTimezone(ctx context.Context, userType domain.UserType, userID string) *time.Location {
	if a.rateUserProfileRepository == nil {
		return nil
	}
	profile, err := a.rateUserProfileRepository.ObtainRateUserProfileByUserID(ctx, userType, userID)
	if err != nil {
		if errors.Is(err, internal.ErrNotFound) {
			return nil
		}
		fmt.Fprintf(a.logger, "notification check: profile lookup chat_id=%s: %v\n", userID, err)
		return nil
	}
	if profile == nil || profile.Timezone == "" {
		return nil
	}
	loc, err := time.LoadLocation(profile.Timezone)
	if err != nil {
		fmt.Fprintf(a.logger, "notification check: unknown timezone chat_id=%s tz=%q: %v\n", userID, profile.Timezone, err)
		return nil
	}
	return loc
}

// triggerOrder defines the canonical sort position for each condition type.
var triggerOrder = map[domain.SubscriptionConditionType]int{
	domain.ConditionTypeDelta:    0,
	domain.ConditionTypeInterval: 1,
	domain.ConditionTypeDaily:    2,
	domain.ConditionTypeCron:     3,
}

// dedupBucket holds one alert and the collapsed trigger values per condition type
// fired on the same (base, quote, kind) key for one user. It may accumulate
// contributions from multiple sources under cross-source dedup; the alert's
// price+delta always belong to the single winning source.
type dedupBucket struct {
	a        alert
	triggers map[domain.SubscriptionConditionType]string
}

// singleSourceName returns the SourceName shared by all alerts, or "" when they
// span multiple sources or the slice is empty. Attributes an event to its source
// for 1:1 delivery; "" leaves attribution empty when one message collapses several sources.
func singleSourceName(alerts []alert) string {
	if len(alerts) == 0 {
		return ""
	}
	name := alerts[0].SourceName
	for _, a := range alerts[1:] {
		if a.SourceName != name {
			return ""
		}
	}
	return name
}

// sortedTriggers converts a collapsed triggers map into a slice in canonical order
// (delta, interval, daily, cron).
func sortedTriggers(m map[domain.SubscriptionConditionType]string) []alertTrigger {
	result := make([]alertTrigger, 0, len(m))
	for ct, cv := range m {
		result = append(result, alertTrigger{ConditionType: ct, ConditionValue: cv})
	}
	sort.Slice(result, func(i, j int) bool {
		oi := triggerOrder[result[i].ConditionType]
		oj := triggerOrder[result[j].ConditionType]
		return oi < oj
	})
	return result
}

// collapseConditionValue merges an incoming condValue into an existing one per the
// most-specific-wins rule for each condition type:
//   - delta:    lowest numeric threshold (most sensitive).
//   - interval: shortest time.Duration.
//   - daily:    lexicographically-earliest HH:MM:SS.
//   - cron:     smallest weekday digit in parts[4].
//
// If existing is empty the incoming value wins unconditionally.
// On parse failure the existing entry is preferred to avoid silent regressions.
func collapseConditionValue(existing, incoming string, condType domain.SubscriptionConditionType) string {
	if existing == "" {
		return incoming
	}
	switch condType {
	case domain.ConditionTypeDelta:
		eVal, err := strconv.ParseFloat(existing, 64)
		if err != nil {
			return existing
		}
		iVal, err := strconv.ParseFloat(incoming, 64)
		if err != nil {
			return existing
		}
		if iVal < eVal {
			return incoming
		}
		return existing

	case domain.ConditionTypeInterval:
		eDur, err := time.ParseDuration(existing)
		if err != nil {
			return existing
		}
		iDur, err := time.ParseDuration(incoming)
		if err != nil {
			return existing
		}
		if iDur < eDur {
			return incoming
		}
		return existing

	case domain.ConditionTypeDaily:
		if incoming < existing {
			return incoming
		}
		return existing

	case domain.ConditionTypeCron:
		eDay := cronWeekdayDigit(existing)
		if eDay < 0 {
			return existing
		}
		iDay := cronWeekdayDigit(incoming)
		if iDay < 0 {
			return existing
		}
		if iDay < eDay {
			return incoming
		}
		return existing

	default:
		return existing
	}
}

// cronWeekdayDigit parses the weekday field (parts[4]) of a 5-field cron expression
// and returns the digit as int. Returns -1 on parse failure.
func cronWeekdayDigit(expr string) int {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return -1
	}
	d, err := strconv.Atoi(parts[4])
	if err != nil {
		return -1
	}
	return d
}
