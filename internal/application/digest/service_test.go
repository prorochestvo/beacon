package digest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // embed IANA tzdata so LoadLocation works without system tzdata

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ SubscriptionsLoader = (*stubSubscriptions)(nil)
	_ SourcesLoader       = (*stubSources)(nil)
	_ ValuesLoader        = (*stubValues)(nil)
	_ ProfilesLoader      = (*stubProfiles)(nil)
)

const testUserID = "123456789"

// testNow is the instant every digest below is rendered at, so the timestamp in
// the output is a fact rather than whatever the clock said.
var testNow = time.Date(2026, 8, 18, 9, 30, 0, 0, time.UTC)

type stubSubscriptions struct {
	subs []domain.RateUserSubscription
	err  error

	askedUserType domain.UserType
	askedUserID   string
}

func (s *stubSubscriptions) ObtainRateUserSubscriptionsByUserID(_ context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error) {
	s.askedUserType, s.askedUserID = userType, userID
	return s.subs, s.err
}

type stubSources struct {
	sources map[string]domain.RateSource
	err     error

	calls int
}

func (s *stubSources) ObtainRateSourcesByNames(_ context.Context, names []string) (map[string]domain.RateSource, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]domain.RateSource, len(names))
	for _, n := range names {
		if src, ok := s.sources[n]; ok {
			out[n] = src
		}
	}
	return out, nil
}

// stubValues answers from prices, records every source name it was asked for so
// a test can prove one lookup per distinct source, and can fail the whole read.
type stubValues struct {
	prices map[string]float64
	err    error

	askedNames []string
}

func (s *stubValues) ObtainLastNRateValuesBySourceName(_ context.Context, name string, _ int64) ([]domain.RateValue, error) {
	s.askedNames = append(s.askedNames, name)
	if s.err != nil {
		return nil, s.err
	}
	price, ok := s.prices[name]
	if !ok {
		return nil, nil
	}
	return []domain.RateValue{{Price: price}}, nil
}

type stubProfiles struct {
	profile *domain.RateUserProfile
	err     error
}

func (s *stubProfiles) ObtainRateUserProfileByUserID(_ context.Context, _ domain.UserType, _ string) (*domain.RateUserProfile, error) {
	return s.profile, s.err
}

// bidSub is one delta subscription against sourceName, already notified once at
// latestRate — the ordinary shape, where the digest has a previous price to
// compute a delta against.
func bidSub(sourceName string, latestRate float64) domain.RateUserSubscription {
	return domain.RateUserSubscription{
		UserType:           domain.UserTypeTelegram,
		UserID:             testUserID,
		SourceName:         sourceName,
		ConditionType:      domain.ConditionTypeDelta,
		ConditionValue:     "5",
		LatestNotifiedRate: latestRate,
	}
}

func bidSource(name string) domain.RateSource {
	return domain.RateSource{
		Name:          name,
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
		Kind:          domain.RateSourceKindBID,
	}
}

// newService wires a Service over the given doubles with the clock pinned and
// logging discarded.
func newService(subs *stubSubscriptions, sources *stubSources, values *stubValues, profiles ProfilesLoader) *Service {
	return NewService(subs, sources, values, profiles, func() time.Time { return testNow }, io.Discard)
}

func TestService_ObtainMeDigest(t *testing.T) {
	t.Parallel()

	t.Run("prices the caller's subscriptions and renders the table", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{subs: []domain.RateUserSubscription{bidSub("src1", 480)}}
		got, err := newService(
			subs,
			&stubSources{sources: map[string]domain.RateSource{"src1": bidSource("src1")}},
			&stubValues{prices: map[string]float64{"src1": 487.55}},
			nil,
		).ObtainMeDigest(t.Context(), testUserID)
		require.NoError(t, err)

		require.Len(t, got.Parts, 1)
		assert.Equal(t, 1, got.Subscriptions)
		assert.Equal(t, 1, got.Priced)
		assert.Contains(t, got.Parts[0], "<pre>")
		assert.Contains(t, got.Parts[0], "USD/KZT")
		assert.Contains(t, got.Parts[0], "FX rates")
		// The digest is on demand, not a trigger fire, so it carries no reason tag.
		for _, tag := range []string{"#DELTA", "#DAILY", "#CRON", "#INTERVAL"} {
			assert.NotContains(t, got.Parts[0], tag)
		}

		assert.Equal(t, domain.UserTypeTelegram, subs.askedUserType)
		assert.Equal(t, testUserID, subs.askedUserID)
	})

	t.Run("a source never notified yet still renders, with no delta", func(t *testing.T) {
		t.Parallel()

		got, err := newService(
			// LatestNotifiedRate zero is the first-fire guard, not a price of zero.
			&stubSubscriptions{subs: []domain.RateUserSubscription{bidSub("src1", 0)}},
			&stubSources{sources: map[string]domain.RateSource{"src1": bidSource("src1")}},
			&stubValues{prices: map[string]float64{"src1": 487.55}},
			nil,
		).ObtainMeDigest(t.Context(), testUserID)
		require.NoError(t, err)

		require.Len(t, got.Parts, 1)
		assert.Contains(t, got.Parts[0], "USD/KZT")
	})

	t.Run("no subscriptions is an empty digest, not an error", func(t *testing.T) {
		t.Parallel()

		got, err := newService(&stubSubscriptions{}, &stubSources{}, &stubValues{}, nil).
			ObtainMeDigest(t.Context(), testUserID)
		require.NoError(t, err)

		assert.Empty(t, got.Parts)
		assert.Zero(t, got.Subscriptions, "the count is what lets a caller tell this from an unpriced account")
		assert.Zero(t, got.Priced)
	})

	t.Run("subscriptions with nothing priced report the count without parts", func(t *testing.T) {
		t.Parallel()

		unpriceable := map[string]struct {
			sources map[string]domain.RateSource
			prices  map[string]float64
		}{
			"source has never been collected": {
				sources: map[string]domain.RateSource{"src1": bidSource("src1")},
				prices:  map[string]float64{},
			},
			"source row is gone": {
				sources: map[string]domain.RateSource{},
				prices:  map[string]float64{"src1": 487},
			},
		}
		for name, tc := range unpriceable {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				got, err := newService(
					&stubSubscriptions{subs: []domain.RateUserSubscription{bidSub("src1", 480)}},
					&stubSources{sources: tc.sources},
					&stubValues{prices: tc.prices},
					nil,
				).ObtainMeDigest(t.Context(), testUserID)
				require.NoError(t, err)

				assert.Empty(t, got.Parts)
				assert.Equal(t, 1, got.Subscriptions,
					"the user does hold a subscription; only the price is missing, and the caller says so differently")
				assert.Zero(t, got.Priced)
			})
		}
	})

	t.Run("one unreadable source costs its own rows, not the digest", func(t *testing.T) {
		t.Parallel()

		// The value read fails for every source here, so nothing is priced — but
		// the call still succeeds and reports what it held.
		got, err := newService(
			&stubSubscriptions{subs: []domain.RateUserSubscription{bidSub("src1", 480)}},
			&stubSources{sources: map[string]domain.RateSource{"src1": bidSource("src1")}},
			&stubValues{err: errors.New("db down")},
			nil,
		).ObtainMeDigest(t.Context(), testUserID)

		require.NoError(t, err, "a rate-value read is per source; failing one must not fail the summary")
		assert.Empty(t, got.Parts)
		assert.Equal(t, 1, got.Subscriptions)
	})

	t.Run("one price lookup per distinct source, however many conditions ride on it", func(t *testing.T) {
		t.Parallel()

		values := &stubValues{prices: map[string]float64{"src1": 487.55}}
		sources := &stubSources{sources: map[string]domain.RateSource{"src1": bidSource("src1")}}
		_, err := newService(
			&stubSubscriptions{subs: []domain.RateUserSubscription{
				bidSub("src1", 480), bidSub("src1", 481), bidSub("src1", 482),
			}},
			sources, values, nil,
		).ObtainMeDigest(t.Context(), testUserID)
		require.NoError(t, err)

		assert.Equal(t, []string{"src1"}, values.askedNames,
			"three conditions on one source is one price, not three round-trips")
		assert.Equal(t, 1, sources.calls, "source metadata is one batched read")
	})

	t.Run("two sources for one pair collapse to a row keeping the BID maximum", func(t *testing.T) {
		t.Parallel()

		got, err := newService(
			&stubSubscriptions{subs: []domain.RateUserSubscription{bidSub("S_HIGH", 0), bidSub("S_LOW", 0)}},
			&stubSources{sources: map[string]domain.RateSource{
				"S_HIGH": bidSource("S_HIGH"),
				"S_LOW":  bidSource("S_LOW"),
			}},
			&stubValues{prices: map[string]float64{"S_HIGH": 490, "S_LOW": 488}},
			nil,
		).ObtainMeDigest(t.Context(), testUserID)
		require.NoError(t, err)

		require.Len(t, got.Parts, 1)
		assert.Equal(t, 1, strings.Count(got.Parts[0], "USD/KZT"), "both sources serve one pair, so one row")
		assert.Contains(t, got.Parts[0], "490")
		assert.NotContains(t, got.Parts[0], "488", "BID keeps the maximum; the loser price must be absent")
		assert.Equal(t, 2, got.Priced, "the counts describe subscriptions, not rendered rows")
	})

	t.Run("a heavy account splits into parts", func(t *testing.T) {
		t.Parallel()

		// Eighty, because the split lands between 60 and 80 rows at a 2048-byte
		// part limit. The test this replaces used 60 and asserted on the count of
		// sent messages plus keyboards, which is 2 for an ordinary single-part
		// send — so it passed without ever producing a second part.
		const subCount = 80
		subs := make([]domain.RateUserSubscription, 0, subCount)
		sources := make(map[string]domain.RateSource, subCount)
		prices := make(map[string]float64, subCount)
		for i := range subCount {
			name := fmt.Sprintf("source_%03d", i)
			subs = append(subs, bidSub(name, 480))
			// A distinct quote per source so dedup cannot collapse them.
			sources[name] = domain.RateSource{
				Name:          name,
				BaseCurrency:  "USD",
				QuoteCurrency: fmt.Sprintf("XX%d", i),
				Kind:          domain.RateSourceKindBID,
			}
			prices[name] = 487.55
		}

		got, err := newService(
			&stubSubscriptions{subs: subs},
			&stubSources{sources: sources},
			&stubValues{prices: prices},
			nil,
		).ObtainMeDigest(t.Context(), testUserID)
		require.NoError(t, err)

		require.Greater(t, len(got.Parts), 1, "one message cannot hold this account, and the split belongs here")
		for _, part := range got.Parts {
			assert.Contains(t, part, "<pre>", "every part carries a rendered table block")
		}
		assert.Equal(t, subCount, got.Priced)
	})

	t.Run("a store the digest cannot do without is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		withSubs := func() *stubSubscriptions {
			return &stubSubscriptions{subs: []domain.RateUserSubscription{bidSub("src1", 480)}}
		}
		broken := map[string]*Service{
			"subscriptions": newService(&stubSubscriptions{err: down}, &stubSources{}, &stubValues{}, nil),
			"sources":       newService(withSubs(), &stubSources{err: down}, &stubValues{}, nil),
		}
		for name, svc := range broken {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				got, err := svc.ObtainMeDigest(t.Context(), testUserID)
				require.ErrorIs(t, err, down)
				assert.Empty(t, got.Parts)
				assert.Zero(t, got.Subscriptions, "a failed read reports nothing rather than a count it never established")
			})
		}
	})
}

// TestService_ObtainMeDigest_Timezone covers the one thing the profile store is
// consulted for. Every failure below has to resolve to UTC rather than to an
// error: a digest in the wrong timezone still carries the prices.
func TestService_ObtainMeDigest_Timezone(t *testing.T) {
	t.Parallel()

	render := func(t *testing.T, profiles ProfilesLoader) string {
		t.Helper()
		got, err := newService(
			&stubSubscriptions{subs: []domain.RateUserSubscription{bidSub("src1", 480)}},
			&stubSources{sources: map[string]domain.RateSource{"src1": bidSource("src1")}},
			&stubValues{prices: map[string]float64{"src1": 487.55}},
			profiles,
		).ObtainMeDigest(t.Context(), testUserID)
		require.NoError(t, err)
		require.Len(t, got.Parts, 1)
		return got.Parts[0]
	}

	t.Run("renders in the stored timezone", func(t *testing.T) {
		t.Parallel()
		// Asia/Almaty is UTC+5.
		assert.Contains(t, render(t, &stubProfiles{profile: &domain.RateUserProfile{Timezone: "Asia/Almaty"}}), "+05")
	})

	t.Run("falls back to UTC", func(t *testing.T) {
		t.Parallel()

		utc := map[string]ProfilesLoader{
			"no profile store wired": nil,
			"no profile row":         &stubProfiles{err: internal.ErrNotFound},
			"blank timezone":         &stubProfiles{profile: &domain.RateUserProfile{}},
			"unknown zone name":      &stubProfiles{profile: &domain.RateUserProfile{Timezone: "Not/AReal/Zone"}},
			"lookup failed":          &stubProfiles{err: errors.New("db down")},
		}
		for name, profiles := range utc {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				assert.Contains(t, render(t, profiles), "+00")
			})
		}
	})
}

func TestNewService_Defaults(t *testing.T) {
	t.Parallel()

	// A nil clock and a nil logger are conveniences for callers that do not care,
	// not wiring mistakes — and neither may panic on first use.
	svc := NewService(
		&stubSubscriptions{subs: []domain.RateUserSubscription{bidSub("src1", 480)}},
		&stubSources{sources: map[string]domain.RateSource{"src1": bidSource("src1")}},
		&stubValues{prices: map[string]float64{"src1": 487.55}},
		nil, nil, nil,
	)

	got, err := svc.ObtainMeDigest(t.Context(), testUserID)
	require.NoError(t, err)
	require.Len(t, got.Parts, 1)
}
