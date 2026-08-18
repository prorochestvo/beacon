package subscription

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ SubscriptionsStore = (*stubSubscriptions)(nil)
	_ SourcesLoader      = (*stubSources)(nil)
	_ ValuesLoader       = (*stubValues)(nil)
)

// stubSubscriptions serves subscriptions keyed by user id and records the
// (userType, userID) pair it was asked for, so a test can prove the service
// scopes the read to one caller. byID backs the ownership look-up, and a miss
// answers (nil, nil) exactly as the repository does.
type stubSubscriptions struct {
	byUser map[string][]domain.RateUserSubscription
	byID   map[string]*domain.RateUserSubscription
	err    error

	retainErr error
	removeErr error

	retained []*domain.RateUserSubscription
	removed  []*domain.RateUserSubscription

	askedUserType domain.UserType
	askedUserID   string
}

func (s *stubSubscriptions) ObtainRateUserSubscriptionsByUserID(_ context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error) {
	s.askedUserType = userType
	s.askedUserID = userID
	if s.err != nil {
		return nil, s.err
	}
	// Copy: the service sorts in place, and a shared backing array would leak
	// that ordering into the next call.
	return append([]domain.RateUserSubscription(nil), s.byUser[userID]...), nil
}

func (s *stubSubscriptions) ObtainRateUserSubscriptionByID(_ context.Context, id string) (*domain.RateUserSubscription, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.byID[id], nil
}

func (s *stubSubscriptions) RetainRateUserSubscription(_ context.Context, record *domain.RateUserSubscription) error {
	if s.retainErr != nil {
		return s.retainErr
	}
	if record.ID == "" {
		record.ID = "generated-id"
	}
	s.retained = append(s.retained, record)
	return nil
}

func (s *stubSubscriptions) RemoveRateUserSubscription(_ context.Context, record *domain.RateUserSubscription) error {
	if s.removeErr != nil {
		return s.removeErr
	}
	s.removed = append(s.removed, record)
	return nil
}

// stubSources resolves source metadata; a name absent from sources is simply
// missing from the returned map (or a nil record), as the repository behaves.
type stubSources struct {
	sources map[string]domain.RateSource
	err     error
}

func (s *stubSources) ObtainRateSourcesByNames(_ context.Context, names []string) (map[string]domain.RateSource, error) {
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

func (s *stubSources) ObtainRateSourceByName(_ context.Context, name string) (*domain.RateSource, error) {
	if s.err != nil {
		return nil, s.err
	}
	if src, ok := s.sources[name]; ok {
		return &src, nil
	}
	return nil, nil //nolint:nilnil // the repository reports a missing source as (nil, nil); the stub has to match it
}

// stubValues resolves the latest value per source and records the names it was
// asked for, so a test can prove only the rendered page is loaded.
type stubValues struct {
	values map[string]domain.RateValue
	err    error

	askedNames []string
}

func (s *stubValues) ObtainLatestRateValuesBySourceNames(_ context.Context, names []string) (map[string]domain.RateValue, error) {
	s.askedNames = append(s.askedNames, names...)
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]domain.RateValue, len(names))
	for _, n := range names {
		if rv, ok := s.values[n]; ok {
			out[n] = rv
		}
	}
	return out, nil
}

func TestService_ObtainMeSubscriptions(t *testing.T) {
	t.Parallel()

	const caller = "111"
	const other = "222"

	collectedAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	t.Run("returns only the caller's rows, enriched with source and latest value", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
			caller: {{ID: "sub1", SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"}},
			other:  {{ID: "sub2", SourceName: "src_b", ConditionType: "interval", ConditionValue: "1h"}},
		}}
		sources := &stubSources{sources: map[string]domain.RateSource{
			"src_a": {Name: "src_a", Title: "Source A", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}}
		values := &stubValues{values: map[string]domain.RateValue{
			"src_a": {Price: 470.5, Timestamp: collectedAt},
		}}

		rows, total, err := NewService(subs, sources, values).ObtainMeSubscriptions(t.Context(), caller, "", 1, 10)
		require.NoError(t, err)

		require.Equal(t, int64(1), total)
		require.Len(t, rows, 1)
		assert.Equal(t, "src_a", rows[0].SourceName)
		assert.Equal(t, "Source A", rows[0].SourceTitle)
		assert.Equal(t, "USD", rows[0].BaseCurrency)
		assert.Equal(t, "KZT", rows[0].QuoteCurrency)
		assert.Equal(t, []string{"delta:5"}, rows[0].Conditions)
		assert.InDelta(t, 470.5, rows[0].LatestPrice, 0.001)
		assert.Equal(t, collectedAt, rows[0].LatestAt)

		assert.Equal(t, domain.UserTypeTelegram, subs.askedUserType)
		assert.Equal(t, caller, subs.askedUserID)
	})

	t.Run("collapses every condition on one source into a single row", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
			caller: {
				{SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"},
				{SourceName: "src_b", ConditionType: "daily", ConditionValue: "9"},
				{SourceName: "src_a", ConditionType: "interval", ConditionValue: "1h"},
			},
		}}

		rows, total, err := NewService(subs, &stubSources{}, &stubValues{}).
			ObtainMeSubscriptions(t.Context(), caller, "", 1, 10)
		require.NoError(t, err)

		require.Equal(t, int64(2), total)
		require.Len(t, rows, 2)
		assert.Equal(t, "src_a", rows[0].SourceName, "groups keep the order their source was first seen in")
		assert.Equal(t, []string{"delta:5", "interval:1h"}, rows[0].Conditions)
		assert.Equal(t, "src_b", rows[1].SourceName)
		assert.Equal(t, []string{"daily:9"}, rows[1].Conditions)
	})

	t.Run("an uncollected source leaves the timestamp zero", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
			caller: {{SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"}},
		}}

		rows, _, err := NewService(subs, &stubSources{}, &stubValues{}).
			ObtainMeSubscriptions(t.Context(), caller, "", 1, 10)
		require.NoError(t, err)

		require.Len(t, rows, 1)
		assert.True(t, rows[0].LatestAt.IsZero(),
			"a source that has never been collected must be distinguishable from one collected at the epoch")
		assert.Zero(t, rows[0].LatestPrice)
	})

	t.Run("search matches title, name and pair label case-insensitively", func(t *testing.T) {
		t.Parallel()

		sources := &stubSources{sources: map[string]domain.RateSource{
			"src_a": {Name: "src_a", Title: "Euro Bank", BaseCurrency: "EUR", QuoteCurrency: "KZT"},
			"src_b": {Name: "src_b", Title: "Dollar Bank", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}}
		newSubs := func() *stubSubscriptions {
			return &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
				caller: {
					{SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"},
					{SourceName: "src_b", ConditionType: "interval", ConditionValue: "1h"},
				},
			}}
		}

		matched := map[string]string{
			"by title":       "euro",
			"by source name": "SRC_A",
			"by pair label":  "eur/kzt",
		}
		for name, query := range matched {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				rows, total, err := NewService(newSubs(), sources, &stubValues{}).
					ObtainMeSubscriptions(t.Context(), caller, query, 1, 10)
				require.NoError(t, err)

				require.Equal(t, int64(1), total)
				require.Len(t, rows, 1)
				assert.Equal(t, "src_a", rows[0].SourceName)
			})
		}
	})

	t.Run("a search drops a row whose source cannot be resolved, an empty query keeps it", func(t *testing.T) {
		t.Parallel()

		newSubs := func() *stubSubscriptions {
			return &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
				caller: {{SourceName: "vanished", ConditionType: "delta", ConditionValue: "5"}},
			}}
		}
		// The source row is gone; the subscription that referenced it is not.
		sources := &stubSources{sources: map[string]domain.RateSource{}}

		rows, total, err := NewService(newSubs(), sources, &stubValues{}).
			ObtainMeSubscriptions(t.Context(), caller, "", 1, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), total)
		require.Len(t, rows, 1, "an unlisted source must not silently hide a subscription the user still holds")
		assert.Empty(t, rows[0].SourceTitle)

		rows, total, err = NewService(newSubs(), sources, &stubValues{}).
			ObtainMeSubscriptions(t.Context(), caller, "anything", 1, 10)
		require.NoError(t, err)
		assert.Zero(t, total, "a search cannot match metadata it cannot read")
		assert.Empty(t, rows)
	})

	t.Run("paginates and loads values only for the rendered page", func(t *testing.T) {
		t.Parallel()

		subs := make([]domain.RateUserSubscription, 12)
		sources := make(map[string]domain.RateSource, 12)
		for i := range 12 {
			name := "src_" + strconv.Itoa(i)
			subs[i] = domain.RateUserSubscription{SourceName: name, ConditionType: "delta", ConditionValue: "1"}
			sources[name] = domain.RateSource{Name: name, Title: "Source " + strconv.Itoa(i)}
		}
		values := &stubValues{}

		rows, total, err := NewService(
			&stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{caller: subs}},
			&stubSources{sources: sources},
			values,
		).ObtainMeSubscriptions(t.Context(), caller, "", 2, 10)
		require.NoError(t, err)

		require.Equal(t, int64(12), total)
		require.Len(t, rows, 2, "page 2 of 10-per-page over 12 rows holds the remaining 2")
		assert.Equal(t, "src_10", rows[0].SourceName)
		assert.Len(t, values.askedNames, 2,
			"loading latest values for the whole list rather than the page is the N+1 this grouping exists to avoid")
	})

	t.Run("a page past the end is empty, not an error", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
			caller: {{SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"}},
		}}

		rows, total, err := NewService(subs, &stubSources{}, &stubValues{}).
			ObtainMeSubscriptions(t.Context(), caller, "", 99, 10)
		require.NoError(t, err)
		assert.Equal(t, int64(1), total, "the total describes the match, not the page")
		assert.Empty(t, rows)
	})

	t.Run("a page before the first is empty, not a panic", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
			caller: {{SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"}},
		}}

		// page 0 cannot arrive from the HTTP handler, which clamps it. This is a
		// package boundary now, and a negative offset would slice out of range.
		rows, _, err := NewService(subs, &stubSources{}, &stubValues{}).
			ObtainMeSubscriptions(t.Context(), caller, "", 0, 10)
		require.NoError(t, err)
		assert.Empty(t, rows)
	})

	t.Run("a failure in any store is reported, not rendered as an empty list", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		withSubs := func() *stubSubscriptions {
			return &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
				caller: {{SourceName: "src_a", ConditionType: "delta", ConditionValue: "5"}},
			}}
		}

		broken := map[string]*Service{
			"subscriptions": NewService(&stubSubscriptions{err: down}, &stubSources{}, &stubValues{}),
			"sources":       NewService(withSubs(), &stubSources{err: down}, &stubValues{}),
			"values":        NewService(withSubs(), &stubSources{}, &stubValues{err: down}),
		}
		for name, svc := range broken {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				rows, total, err := svc.ObtainMeSubscriptions(t.Context(), caller, "", 1, 10)
				require.ErrorIs(t, err, down)
				assert.Nil(t, rows)
				assert.Zero(t, total)
			})
		}
	})
}

func TestService_ObtainMeSubscriptionsRaw(t *testing.T) {
	t.Parallel()

	const caller = "555"

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	sources := &stubSources{sources: map[string]domain.RateSource{
		"src_a": {Name: "src_a", Title: "Alpha", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		"src_b": {Name: "src_b", Title: "Beta", BaseCurrency: "EUR", QuoteCurrency: "KZT"},
	}}

	t.Run("one row per condition, carrying the id and the source metadata", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
			caller: {
				{ID: "id-1", SourceName: "src_a", ConditionType: "delta", ConditionValue: "0.5", UpdatedAt: now},
				{ID: "id-2", SourceName: "src_a", ConditionType: "interval", ConditionValue: "1h", UpdatedAt: now},
			},
		}}

		rows, err := NewService(subs, sources, &stubValues{}).ObtainMeSubscriptionsRaw(t.Context(), caller)
		require.NoError(t, err)

		require.Len(t, rows, 2, "the editor addresses conditions individually; grouping them would lose the ids")
		assert.Equal(t, "id-1", rows[0].ID)
		assert.Equal(t, "Alpha", rows[0].SourceTitle)
		assert.Equal(t, "USD", rows[0].BaseCurrency)
		assert.Equal(t, "KZT", rows[0].QuoteCurrency)
		assert.Equal(t, domain.SubscriptionConditionType("delta"), rows[0].ConditionType)
		assert.Equal(t, "0.5", rows[0].ConditionValue)
		assert.Equal(t, now, rows[0].UpdatedAt)
	})

	t.Run("ordered source name ascending, then most recently updated first", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
			caller: {
				{ID: "z1", SourceName: "src_b", ConditionType: "delta", ConditionValue: "1", UpdatedAt: now},
				{ID: "z2", SourceName: "src_a", ConditionType: "delta", ConditionValue: "2", UpdatedAt: now.Add(-time.Hour)},
				{ID: "z3", SourceName: "src_a", ConditionType: "interval", ConditionValue: "1h", UpdatedAt: now},
			},
		}}

		rows, err := NewService(subs, sources, &stubValues{}).ObtainMeSubscriptionsRaw(t.Context(), caller)
		require.NoError(t, err)

		require.Len(t, rows, 3)
		assert.Equal(t, []string{"z3", "z2", "z1"}, []string{rows[0].ID, rows[1].ID, rows[2].ID},
			"src_a before src_b, and the newer src_a row first")
	})

	t.Run("no subscriptions yields an empty slice", func(t *testing.T) {
		t.Parallel()

		rows, err := NewService(&stubSubscriptions{}, sources, &stubValues{}).
			ObtainMeSubscriptionsRaw(t.Context(), caller)
		require.NoError(t, err)
		require.NotNil(t, rows)
		assert.Empty(t, rows)
	})

	t.Run("a failure in either store is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		broken := map[string]*Service{
			"subscriptions": NewService(&stubSubscriptions{err: down}, sources, &stubValues{}),
			"sources": NewService(&stubSubscriptions{byUser: map[string][]domain.RateUserSubscription{
				caller: {{ID: "id-1", SourceName: "src_a", ConditionType: "delta", ConditionValue: "1"}},
			}}, &stubSources{err: down}, &stubValues{}),
		}
		for name, svc := range broken {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				rows, err := svc.ObtainMeSubscriptionsRaw(t.Context(), caller)
				require.ErrorIs(t, err, down)
				assert.Nil(t, rows)
			})
		}
	})
}

func TestService_CreateMeSubscription(t *testing.T) {
	t.Parallel()

	const caller = "42"

	sources := func() *stubSources {
		return &stubSources{sources: map[string]domain.RateSource{
			"src_a": {Name: "src_a", Title: "Source A", BaseCurrency: "USD", QuoteCurrency: "KZT", Active: true},
		}}
	}
	valid := NewSubscription{SourceName: "src_a", ConditionType: domain.ConditionTypeDelta, ConditionValue: "5"}

	t.Run("stores the row under the caller and returns the generated id", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{}
		id, err := NewService(subs, sources(), &stubValues{}).CreateMeSubscription(t.Context(), caller, valid)
		require.NoError(t, err)
		assert.NotEmpty(t, id)

		require.Len(t, subs.retained, 1)
		assert.Equal(t, domain.UserTypeTelegram, subs.retained[0].UserType)
		assert.Equal(t, caller, subs.retained[0].UserID, "ownership comes from the caller, never from the request")
		assert.Equal(t, "src_a", subs.retained[0].SourceName)
		assert.Equal(t, domain.ConditionTypeDelta, subs.retained[0].ConditionType)
		assert.Equal(t, "5", subs.retained[0].ConditionValue)
		assert.Equal(t, id, subs.retained[0].ID)
	})

	t.Run("an unknown source is refused before the insert", func(t *testing.T) {
		t.Parallel()

		subs := &stubSubscriptions{}
		req := valid
		req.SourceName = "no_such"
		id, err := NewService(subs, sources(), &stubValues{}).CreateMeSubscription(t.Context(), caller, req)

		var pub *internal.PublicError
		require.ErrorAs(t, err, &pub, "an unknown source is the caller's mistake and has to be shown to them")
		assert.Equal(t, "unknown source", pub.Details())
		assert.Empty(t, id)
		assert.Empty(t, subs.retained, "the foreign key would refuse this anyway, as an error nobody can act on")
	})

	t.Run("an unparseable condition is refused with the type but not the value", func(t *testing.T) {
		t.Parallel()

		unparseable := map[string]NewSubscription{
			"unknown type": {SourceName: "src_a", ConditionType: "bogus", ConditionValue: "5"},
			"delta":        {SourceName: "src_a", ConditionType: domain.ConditionTypeDelta, ConditionValue: "not-a-number"},
			"interval":     {SourceName: "src_a", ConditionType: domain.ConditionTypeInterval, ConditionValue: "30s"},
			"daily":        {SourceName: "src_a", ConditionType: domain.ConditionTypeDaily, ConditionValue: "not-a-time"},
			"cron":         {SourceName: "src_a", ConditionType: domain.ConditionTypeCron, ConditionValue: "* * *"},
		}
		for name, req := range unparseable {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				subs := &stubSubscriptions{}
				_, err := NewService(subs, sources(), &stubValues{}).CreateMeSubscription(t.Context(), caller, req)

				var pub *internal.PublicError
				require.ErrorAs(t, err, &pub)
				assert.Contains(t, pub.Details(), "invalid condition value for "+string(req.ConditionType))
				assert.NotContains(t, pub.Details(), req.ConditionValue,
					"echoing the value back tells the caller nothing they did not just send")
				assert.Empty(t, subs.retained)
			})
		}
	})

	t.Run("a store failure is reported as a failure, not as a message for the caller", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		broken := map[string]*Service{
			"source lookup": NewService(&stubSubscriptions{}, &stubSources{err: down}, &stubValues{}),
			"retain":        NewService(&stubSubscriptions{retainErr: down}, sources(), &stubValues{}),
		}
		for name, svc := range broken {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				id, err := svc.CreateMeSubscription(t.Context(), caller, valid)
				require.ErrorIs(t, err, down)
				assert.Empty(t, id)

				var pub *internal.PublicError
				require.NotErrorAs(t, err, &pub, "a dead database is not something to tell the caller about")
			})
		}
	})
}

func TestService_UpdateMeSubscription(t *testing.T) {
	t.Parallel()

	const caller = "10"
	const other = "20"

	stored := func() *stubSubscriptions {
		return &stubSubscriptions{byID: map[string]*domain.RateUserSubscription{
			"sub-001": {
				ID: "sub-001", UserType: domain.UserTypeTelegram, UserID: caller,
				SourceName: "src_a", ConditionType: domain.ConditionTypeDelta, ConditionValue: "5",
			},
			"sub-other": {
				ID: "sub-other", UserType: domain.UserTypeTelegram, UserID: other,
				SourceName: "src_a", ConditionType: domain.ConditionTypeDelta, ConditionValue: "3",
			},
		}}
	}
	toInterval := ConditionUpdate{ConditionType: domain.ConditionTypeInterval, ConditionValue: "1h"}

	t.Run("rewrites the condition and carries the rest of the row over", func(t *testing.T) {
		t.Parallel()

		subs := stored()
		require.NoError(t, NewService(subs, &stubSources{}, &stubValues{}).
			UpdateMeSubscription(t.Context(), caller, "sub-001", toInterval))

		require.Len(t, subs.retained, 1)
		assert.Equal(t, domain.ConditionTypeInterval, subs.retained[0].ConditionType)
		assert.Equal(t, "1h", subs.retained[0].ConditionValue)
		assert.Equal(t, "sub-001", subs.retained[0].ID, "an update must not mint a new identifier")
		assert.Equal(t, caller, subs.retained[0].UserID)
		assert.Equal(t, "src_a", subs.retained[0].SourceName, "changing source is a delete and a create")
	})

	t.Run("a missing row and another user's row are the same answer", func(t *testing.T) {
		t.Parallel()

		unreachable := map[string]string{
			"no such subscription":  "no-such",
			"owned by another user": "sub-other",
		}
		for name, id := range unreachable {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				subs := stored()
				err := NewService(subs, &stubSources{}, &stubValues{}).
					UpdateMeSubscription(t.Context(), caller, id, toInterval)

				require.ErrorIs(t, err, internal.ErrNotFound,
					"one sentinel for both, so nothing downstream can answer 403 and confirm the row exists")
				assert.Empty(t, subs.retained)
			})
		}
	})

	t.Run("ownership is settled before the condition is judged", func(t *testing.T) {
		t.Parallel()

		subs := stored()
		err := NewService(subs, &stubSources{}, &stubValues{}).UpdateMeSubscription(
			t.Context(), caller, "sub-other",
			ConditionUpdate{ConditionType: "bogus", ConditionValue: "x"},
		)

		require.ErrorIs(t, err, internal.ErrNotFound,
			"validating first would answer 400 for another user's row and 404 for a missing one — a probe")
	})

	t.Run("an unparseable condition is refused and nothing is written", func(t *testing.T) {
		t.Parallel()

		subs := stored()
		err := NewService(subs, &stubSources{}, &stubValues{}).UpdateMeSubscription(
			t.Context(), caller, "sub-001",
			ConditionUpdate{ConditionType: domain.ConditionTypeDelta, ConditionValue: "not-a-number"},
		)

		var pub *internal.PublicError
		require.ErrorAs(t, err, &pub)
		assert.Empty(t, subs.retained)
	})

	t.Run("a store failure is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")

		lookupBroken := stored()
		lookupBroken.err = down
		require.ErrorIs(t, NewService(lookupBroken, &stubSources{}, &stubValues{}).
			UpdateMeSubscription(t.Context(), caller, "sub-001", toInterval), down)

		retainBroken := stored()
		retainBroken.retainErr = down
		require.ErrorIs(t, NewService(retainBroken, &stubSources{}, &stubValues{}).
			UpdateMeSubscription(t.Context(), caller, "sub-001", toInterval), down)
	})
}

func TestService_DeleteMeSubscription(t *testing.T) {
	t.Parallel()

	const caller = "10"
	const other = "20"

	stored := func() *stubSubscriptions {
		return &stubSubscriptions{byID: map[string]*domain.RateUserSubscription{
			"sub-001": {
				ID: "sub-001", UserType: domain.UserTypeTelegram, UserID: caller,
				SourceName: "src_a", ConditionType: domain.ConditionTypeDelta, ConditionValue: "5",
			},
			"sub-other": {
				ID: "sub-other", UserType: domain.UserTypeTelegram, UserID: other,
				SourceName: "src_a", ConditionType: domain.ConditionTypeDelta, ConditionValue: "3",
			},
		}}
	}

	t.Run("removes the stored row", func(t *testing.T) {
		t.Parallel()

		subs := stored()
		require.NoError(t, NewService(subs, &stubSources{}, &stubValues{}).
			DeleteMeSubscription(t.Context(), caller, "sub-001"))

		require.Len(t, subs.removed, 1)
		assert.Equal(t, "sub-001", subs.removed[0].ID)
	})

	t.Run("a missing row and another user's row are the same answer", func(t *testing.T) {
		t.Parallel()

		unreachable := map[string]string{
			"no such subscription":  "no-such",
			"owned by another user": "sub-other",
		}
		for name, id := range unreachable {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				subs := stored()
				err := NewService(subs, &stubSources{}, &stubValues{}).
					DeleteMeSubscription(t.Context(), caller, id)

				require.ErrorIs(t, err, internal.ErrNotFound)
				assert.Empty(t, subs.removed, "another user's subscription must survive the attempt")
			})
		}
	})

	t.Run("a store failure is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")

		lookupBroken := stored()
		lookupBroken.err = down
		require.ErrorIs(t, NewService(lookupBroken, &stubSources{}, &stubValues{}).
			DeleteMeSubscription(t.Context(), caller, "sub-001"), down)

		removeBroken := stored()
		removeBroken.removeErr = down
		require.ErrorIs(t, NewService(removeBroken, &stubSources{}, &stubValues{}).
			DeleteMeSubscription(t.Context(), caller, "sub-001"), down)
	})
}
