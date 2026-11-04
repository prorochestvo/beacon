// Package digest builds one user's on-demand rate summary: everything they
// subscribe to, priced as of now, rendered into the same aligned table the
// scheduled notifier emits.
//
// It is the answer half of the Telegram "Latest updates" button, separated so
// that asking the question does not require a chat — an HTTP endpoint or a
// scheduled job wants the same result. Nothing here decides what to *say*: which
// words meet a user with no subscriptions, or with subscriptions but no prices
// yet, is the caller's to choose from the counts on Digest.
package digest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/application/notification"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// Digest is the state of one user's subscriptions at a moment.
type Digest struct {
	// Parts are the rendered message parts, already split to fit a single
	// Telegram message. Empty when nothing could be priced — which is not the
	// same as holding no subscriptions, and Subscriptions is what tells the two
	// apart.
	Parts []string
	// Subscriptions is how many rows the user holds, counted before any were
	// dropped for want of a source row or a collected price.
	Subscriptions int
	// Priced is how many of those survived into Parts. Subscriptions > 0 with
	// Priced == 0 is the "nothing collected yet" state.
	Priced int
}

// Service assembles a user's digest from the subscription, source, rate-value
// and profile stores. Construct it with NewService; it holds no mutable state
// and is safe for concurrent use.
type Service struct {
	subs     SubscriptionsLoader
	sources  SourcesLoader
	values   ValuesLoader
	profiles ProfilesLoader
	now      func() time.Time
	logger   io.Writer
}

// NewService constructs a Service over the stores a digest reads.
//
// profiles may be nil, and then every digest renders in UTC; that is how a
// deployment without profile storage is meant to behave rather than a wiring
// mistake. now is injected for deterministic tests — pass time.Now in
// production. A nil logger discards.
func NewService(
	subs SubscriptionsLoader,
	sources SourcesLoader,
	values ValuesLoader,
	profiles ProfilesLoader,
	now func() time.Time,
	logger io.Writer,
) *Service {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = io.Discard
	}
	return &Service{subs: subs, sources: sources, values: values, profiles: profiles, now: now, logger: logger}
}

// ObtainMeDigest loads userID's subscriptions, prices them, and renders the
// message parts, reporting alongside them how many rows went in and how many
// survived.
//
// A subscription whose source row is gone, or whose source has never produced a
// value, is dropped rather than failing the digest: one dead pair should not cost
// a user the rest of their summary. An inactive source is kept — a subscription
// to one remains a valid pair until the user deletes it.
func (s *Service) ObtainMeDigest(ctx context.Context, userID string) (Digest, error) {
	subs, err := s.subs.ObtainRateUserSubscriptionsByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		return Digest{}, fmt.Errorf("load subscriptions: %w", err)
	}
	if len(subs) == 0 {
		return Digest{}, nil
	}

	// Collect the distinct source names so metadata is one round-trip rather than
	// one per subscription.
	seen := make(map[string]struct{}, len(subs))
	names := make([]string, 0, len(subs))
	for _, sub := range subs {
		if _, ok := seen[sub.SourceName]; !ok {
			seen[sub.SourceName] = struct{}{}
			names = append(names, sub.SourceName)
		}
	}

	sourceMeta, err := s.sources.ObtainRateSourcesByNames(ctx, names)
	if err != nil {
		return Digest{}, fmt.Errorf("load sources: %w", err)
	}

	// Priced by distinct source name, not per subscription: several conditions on
	// one source would otherwise repeat the same query.
	currentPrices := make(map[string]float64, len(names))
	for _, name := range names {
		values, valuesErr := s.values.ObtainLastNRateValuesBySourceName(ctx, name, 1)
		if valuesErr != nil {
			// One unreadable source costs its own rows, not the whole digest.
			fmt.Fprintf(s.logger, "digest: rate value lookup source=%s chat=%s: %v\n", name, userID, valuesErr)
			continue
		}
		if len(values) == 0 {
			continue
		}
		currentPrices[name] = values[0].Price
	}

	snapshots := make([]notification.SubscriptionSnapshot, 0, len(subs))
	for _, sub := range subs {
		src, ok := sourceMeta[sub.SourceName]
		if !ok {
			continue
		}
		price, ok := currentPrices[sub.SourceName]
		if !ok {
			continue
		}
		snapshots = append(snapshots, notification.SubscriptionSnapshot{
			Subscription: sub,
			Source:       src,
			CurrentPrice: price,
		})
	}

	if len(snapshots) == 0 {
		fmt.Fprintf(s.logger, "digest: no rate data chat=%s subs=%d snapshots=0\n", userID, len(subs))
		return Digest{Subscriptions: len(subs)}, nil
	}

	parts, err := notification.BuildSubscriptionDigest(s.now().UTC(), s.resolveUserTimezone(ctx, userID), snapshots)
	if err != nil {
		return Digest{}, fmt.Errorf("render digest: %w", err)
	}

	return Digest{Parts: parts, Subscriptions: len(subs), Priced: len(snapshots)}, nil
}

// resolveUserTimezone returns the time.Location stored for userID, or nil when no
// profile is configured, the timezone name is unknown to the Go runtime, or the
// lookup fails.
//
// Nil renders the digest in UTC, and every failure below resolves to nil on
// purpose: a digest in the wrong timezone still carries the prices, while an
// error here would cost the user the whole summary over a display detail.
func (s *Service) resolveUserTimezone(ctx context.Context, userID string) *time.Location {
	if s.profiles == nil {
		return nil
	}
	profile, err := s.profiles.ObtainRateUserProfileByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		if errors.Is(err, internal.ErrNotFound) {
			// No profile stored is the ordinary state, not a fault worth a line.
			return nil
		}
		fmt.Fprintf(s.logger, "digest: profile lookup chat_id=%s: %v\n", userID, err)
		return nil
	}
	if profile == nil || profile.Timezone == "" {
		return nil
	}
	loc, err := time.LoadLocation(profile.Timezone)
	if err != nil {
		fmt.Fprintf(s.logger, "digest: unknown timezone chat_id=%s tz=%q: %v\n", userID, profile.Timezone, err)
		return nil
	}
	return loc
}

// SubscriptionsLoader reads the subscription rows one user owns. Satisfied by
// *repository.RateUserSubscriptionRepository.
type SubscriptionsLoader interface {
	ObtainRateUserSubscriptionsByUserID(
		ctx context.Context, userType domain.UserType, userID string,
	) ([]domain.RateUserSubscription, error)
}

// SourcesLoader resolves source metadata for a set of source names. A name with
// no row is absent from the returned map rather than an error.
type SourcesLoader interface {
	ObtainRateSourcesByNames(ctx context.Context, names []string) (map[string]domain.RateSource, error)
}

// ValuesLoader reads the most recent collected values for one source.
type ValuesLoader interface {
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, n int64) ([]domain.RateValue, error)
}

// ProfilesLoader looks up per-user preferences, of which the digest needs only
// the timezone. Implementations report (nil, internal.ErrNotFound) when no row
// exists; that absence is normal and means UTC.
type ProfilesLoader interface {
	ObtainRateUserProfileByUserID(
		ctx context.Context, userType domain.UserType, userID string,
	) (*domain.RateUserProfile, error)
}
