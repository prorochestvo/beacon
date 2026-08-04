package service

import (
	"context"
	"errors"
	"testing"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/stretchr/testify/require"
)

var _ executionHistoryRepository = &repository.ExecutionHistoryRepository{}
var _ rateSourceRepository = &repository.RateSourceRepository{}
var _ RateValuesLoader = &repository.RateValueRepository{}
var _ rateUserSubscriptionRepository = &repository.RateUserSubscriptionRepository{}
var _ rateUserEventRepository = &repository.RateUserEventRepository{}

var _ executionHistoryRepository = (*mockExecutionHistoryRepository)(nil)
var _ rateSourceRepository = (*mockRateSourceRepository)(nil)
var _ RateValuesLoader = (*mockRateValueRepository)(nil)
var _ rateUserSubscriptionRepository = (*mockRateUserSubscriptionRepository)(nil)
var _ rateUserEventRepository = (*mockRateUserEventRepository)(nil)

func TestRateRestApi_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when repo is healthy", func(t *testing.T) {
		t.Parallel()

		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})
		require.NoError(t, svc.CheckUP(t.Context()))
	})
	t.Run("propagates repo error", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateSourceRepository{err: errors.New("db down")}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, repo, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})
		require.Error(t, svc.CheckUP(t.Context()))
	})
}

func TestRateRestApi_ObtainLastNExecutionHistoryBySourceName(t *testing.T) {
	t.Parallel()

	t.Run("delegates and returns results", func(t *testing.T) {
		t.Parallel()

		want := []domain.ExecutionHistory{{ID: "h1", SourceName: "src1", Success: true}}
		repo := &mockExecutionHistoryRepository{items: want}
		svc := newRateRestAPI(t, repo, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		got, err := svc.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src1", 5)
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.False(t, repo.capturedBool)
	})
	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockExecutionHistoryRepository{err: errors.New("db error")}
		svc := newRateRestAPI(t, repo, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		_, err := svc.ObtainLastNExecutionHistoryBySourceName(t.Context(), "src1", 5)
		require.Error(t, err)
	})
}

func TestRateRestApi_ObtainLatestExecutionHistoryBySources(t *testing.T) {
	t.Parallel()

	t.Run("delegates and returns map keyed by source", func(t *testing.T) {
		t.Parallel()

		items := []domain.ExecutionHistory{
			{ID: "h1", SourceName: "src1", Success: true},
			{ID: "h2", SourceName: "src2", Success: false},
		}
		repo := &mockExecutionHistoryRepository{items: items}
		svc := newRateRestAPI(t, repo, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		got, err := svc.ObtainLatestExecutionHistoryBySources(t.Context(), []string{"src1", "src2"})
		require.NoError(t, err)
		require.Len(t, got, 2)
		require.True(t, got["src1"].Success)
		require.False(t, got["src2"].Success)
	})
	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockExecutionHistoryRepository{err: errors.New("db down")}
		svc := newRateRestAPI(t, repo, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		_, err := svc.ObtainLatestExecutionHistoryBySources(t.Context(), []string{"src1"})
		require.Error(t, err)
	})
}

func TestRateRestApi_ObtainLastSuccessNExecutionHistoryBySourceName(t *testing.T) {
	t.Parallel()

	t.Run("calls repo with successOnly=true", func(t *testing.T) {
		t.Parallel()

		repo := &mockExecutionHistoryRepository{items: []domain.ExecutionHistory{{ID: "h1"}}}
		svc := newRateRestAPI(t, repo, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		_, err := svc.ObtainLastSuccessNExecutionHistoryBySourceName(t.Context(), "src1", 5)
		require.NoError(t, err)
		require.True(t, repo.capturedBool)
	})
	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockExecutionHistoryRepository{err: errors.New("fail")}
		svc := newRateRestAPI(t, repo, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		_, err := svc.ObtainLastSuccessNExecutionHistoryBySourceName(t.Context(), "src1", 5)
		require.Error(t, err)
	})
}

func TestRateRestApi_UpdateRateSourceActive(t *testing.T) {
	t.Parallel()

	t.Run("toggles active and persists via repo", func(t *testing.T) {
		t.Parallel()

		src := domain.RateSource{Name: "src1", Active: false}
		repo := &mockRateSourceRepository{sources: []domain.RateSource{src}}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, repo, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		err := svc.UpdateRateSourceActive(t.Context(), "src1", true)
		require.NoError(t, err)
		require.NotNil(t, repo.retainedSrc, "service must call RetainRateSource")
		require.True(t, repo.retainedSrc.Active, "Active=true must be forwarded to repo")
		require.Equal(t, "src1", repo.retainedSrc.Name)
	})

	t.Run("returns ErrNotFound for unknown source", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateSourceRepository{}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, repo, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		err := svc.UpdateRateSourceActive(t.Context(), "no-such-source", true)
		require.Error(t, err)
		require.ErrorIs(t, err, internal.ErrNotFound)
	})

	t.Run("repo read error propagated and not wrapped as ErrNotFound", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateSourceRepository{err: errors.New("db down")}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, repo, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		err := svc.UpdateRateSourceActive(t.Context(), "src1", true)
		require.Error(t, err)
		require.NotErrorIs(t, err, internal.ErrNotFound)
	})
}

func TestRateRestApi_ObtainAllRateSources(t *testing.T) {
	t.Parallel()

	t.Run("returns sources", func(t *testing.T) {
		t.Parallel()

		want := []domain.RateSource{{Name: "src1"}, {Name: "src2"}}
		repo := &mockRateSourceRepository{sources: want}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, repo, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		got, err := svc.ObtainAllRateSources(t.Context())
		require.NoError(t, err)
		require.Equal(t, want, got)
	})
	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateSourceRepository{err: errors.New("fail")}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, repo, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		_, err := svc.ObtainAllRateSources(t.Context())
		require.Error(t, err)
	})
}

func TestRateRestApi_ObtainLastNRateValuesBySourceName(t *testing.T) {
	t.Parallel()

	t.Run("delegates with correct args", func(t *testing.T) {
		t.Parallel()

		want := []domain.RateValue{{ID: "v1", Price: 470.0}}
		repo := &mockRateValueRepository{values: want}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, repo, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		got, err := svc.ObtainLastNRateValuesBySourceName(t.Context(), "src1", 10)
		require.NoError(t, err)
		require.Equal(t, want, got)
	})
	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateValueRepository{err: errors.New("fail")}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, repo, &mockRateUserSubscriptionRepository{}, &mockRateUserEventRepository{})

		_, err := svc.ObtainLastNRateValuesBySourceName(t.Context(), "src1", 10)
		require.Error(t, err)
	})
}

func TestRateRestApi_ObtainListOfLastRateUserEvent(t *testing.T) {
	t.Parallel()

	t.Run("calls repo with all four statuses", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, repo)

		_, err := svc.ObtainListOfLastRateUserEvent(t.Context(), 10)
		require.NoError(t, err)

		require.Contains(t, repo.lastNStatuses, domain.RateUserEventStatusPending)
		require.Contains(t, repo.lastNStatuses, domain.RateUserEventStatusSent)
		require.Contains(t, repo.lastNStatuses, domain.RateUserEventStatusFailed)
		require.Contains(t, repo.lastNStatuses, domain.RateUserEventStatusCanceled)
	})
	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{err: errors.New("fail")}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, repo)

		_, err := svc.ObtainListOfLastRateUserEvent(t.Context(), 10)
		require.Error(t, err)
	})
}

func TestRateRestApi_ObtainFailedListOfRateUserEvent(t *testing.T) {
	t.Parallel()

	t.Run("delegates with offset, limit and failed status", func(t *testing.T) {
		t.Parallel()

		want := []domain.RateUserEvent{{ID: "e1"}}
		repo := &mockRateUserEventRepository{items: want}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, repo)

		got, err := svc.ObtainFailedListOfRateUserEvent(t.Context(), 0, 10)
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.Equal(t, []domain.RateUserEventStatus{domain.RateUserEventStatusFailed}, repo.lastNStatuses)
	})
	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{err: errors.New("fail")}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, repo)

		_, err := svc.ObtainFailedListOfRateUserEvent(t.Context(), 0, 10)
		require.Error(t, err)
	})
}

func TestRateRestApi_ObtainPendingRateUserEvents(t *testing.T) {
	t.Parallel()

	t.Run("calls repo with pending status only", func(t *testing.T) {
		t.Parallel()

		want := []domain.RateUserEvent{{ID: "p1", Status: domain.RateUserEventStatusPending}}
		repo := &mockRateUserEventRepository{items: want}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, repo)

		got, err := svc.ObtainPendingRateUserEvents(t.Context())
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.Equal(t, []domain.RateUserEventStatus{domain.RateUserEventStatusPending}, repo.lastNStatuses)
	})
	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{err: errors.New("fail")}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, repo)

		_, err := svc.ObtainPendingRateUserEvents(t.Context())
		require.Error(t, err)
	})
}

func TestRateRestApi_ObtainFailedRateUserEventsBySourceName(t *testing.T) {
	t.Parallel()

	t.Run("calculates offset from page and returns failed events", func(t *testing.T) {
		t.Parallel()

		want := []domain.RateUserEvent{{ID: "e1"}}
		repo := &mockRateUserEventRepository{items: want}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, repo)

		got, err := svc.ObtainFailedRateUserEventsBySourceName(t.Context(), "src1", 1, 50)
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.Equal(t, []domain.RateUserEventStatus{domain.RateUserEventStatusFailed}, repo.bySourceStatuses)
	})
	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserEventRepository{err: errors.New("fail")}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, &mockRateUserSubscriptionRepository{}, repo)

		_, err := svc.ObtainFailedRateUserEventsBySourceName(t.Context(), "src1", 1, 50)
		require.Error(t, err)
	})
}

func TestRateRestApi_ObtainStats(t *testing.T) {
	t.Parallel()
	t.Skip("not implemented yet")
}

func TestRateRestApi_ObtainRateUserSubscriptionsBySourcePaged(t *testing.T) {
	t.Parallel()
	t.Skip("not implemented yet")
}

func TestRateRestApi_ObtainDailyEventSummaryBySource(t *testing.T) {
	t.Parallel()
	t.Skip("not implemented yet")
}
func TestRateRestApi_ObtainLastNExecutionHistoryErrors(t *testing.T) {
	t.Parallel()
	t.Skip("not implemented yet")
}

func TestRateRestApi_ObtainSubscriptionSummaryBySource(t *testing.T) {
	t.Parallel()

	t.Run("delegates and returns summaries", func(t *testing.T) {
		t.Parallel()

		want := []domain.RateUserSubscriptionSummary{
			{SourceName: "src1", UserType: domain.UserTypeTelegram, SubscriptionCount: 3},
		}
		repo := &mockRateUserSubscriptionRepository{summaries: want}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, repo, &mockRateUserEventRepository{})

		got, err := svc.ObtainSubscriptionSummaryBySource(t.Context(), "src1")
		require.NoError(t, err)
		require.Equal(t, want, got)
	})

	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()

		repo := &mockRateUserSubscriptionRepository{err: errors.New("fail")}
		svc := newRateRestAPI(t, &mockExecutionHistoryRepository{}, &mockRateSourceRepository{}, &mockRateValueRepository{}, repo, &mockRateUserEventRepository{})

		_, err := svc.ObtainSubscriptionSummaryBySource(t.Context(), "src1")
		require.Error(t, err)
	})
}

func newRateRestAPI(
	t *testing.T,
	eh executionHistoryRepository,
	rs rateSourceRepository,
	rv RateValuesLoader,
	rus rateUserSubscriptionRepository,
	rue rateUserEventRepository,
) *RateRestApi {
	t.Helper()
	svc, err := NewRateRestAPI(eh, rs, rv, rus, rue)
	require.NoError(t, err)
	return svc
}

type mockExecutionHistoryRepository struct {
	items        []domain.ExecutionHistory
	err          error
	capturedBool bool
	errorCount   int64
}

func (m *mockExecutionHistoryRepository) ObtainLastNExecutionHistoryBySourceName(_ context.Context, _ string, _ int64, s bool) ([]domain.ExecutionHistory, error) {
	m.capturedBool = s
	return m.items, m.err
}

func (m *mockExecutionHistoryRepository) ObtainLatestExecutionHistoryBySources(_ context.Context, names []string) (map[string]domain.ExecutionHistory, error) {
	if m.err != nil {
		return nil, m.err
	}
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	out := make(map[string]domain.ExecutionHistory, len(names))
	for _, h := range m.items {
		if _, ok := want[h.SourceName]; ok {
			out[h.SourceName] = h
		}
	}
	return out, nil
}

func (m *mockExecutionHistoryRepository) ObtainExecutionHistoryErrorCount(_ context.Context) (int64, error) {
	return m.errorCount, m.err
}

func (m *mockExecutionHistoryRepository) ObtainLastNExecutionHistoryErrors(_ context.Context, _, _ int64) ([]domain.ExecutionHistory, error) {
	return m.items, m.err
}

type mockRateSourceRepository struct {
	sources     []domain.RateSource
	err         error
	retainedSrc *domain.RateSource
}

func (m *mockRateSourceRepository) CheckUP(_ context.Context) error {
	return m.err
}

func (m *mockRateSourceRepository) ObtainAllRateSources(_ context.Context) ([]domain.RateSource, error) {
	return m.sources, m.err
}

func (m *mockRateSourceRepository) ObtainRateSourceByName(_ context.Context, n string) (*domain.RateSource, error) {
	if m.err != nil {
		return nil, m.err
	}
	for _, s := range m.sources {
		if s.Name == n {
			return &s, nil
		}
	}
	return nil, nil
}

func (m *mockRateSourceRepository) RetainRateSource(_ context.Context, s *domain.RateSource) error {
	if s != nil {
		cp := *s
		m.retainedSrc = &cp
	}
	return m.err
}

type mockRateValueRepository struct {
	values []domain.RateValue
	err    error
}

func (m *mockRateValueRepository) ObtainLastNRateValuesBySourceName(_ context.Context, _ string, _ int64) ([]domain.RateValue, error) {
	return m.values, m.err
}

type mockRateUserSubscriptionRepository struct {
	summaries []domain.RateUserSubscriptionSummary
	details   []domain.RateUserSubscriptionDetail
	err       error
}

func (m *mockRateUserSubscriptionRepository) ObtainRateUserSubscriptionsBySource(_ context.Context, _ string) ([]domain.RateUserSubscription, error) {
	return nil, m.err
}

func (m *mockRateUserSubscriptionRepository) ObtainSubscriptionSummaryBySource(_ context.Context, _ string) ([]domain.RateUserSubscriptionSummary, error) {
	return m.summaries, m.err
}

func (m *mockRateUserSubscriptionRepository) ObtainRateUserSubscriptionsBySourcePaged(_ context.Context, _ string, _, _ int64) ([]domain.RateUserSubscriptionDetail, error) {
	return m.details, m.err
}

type mockRateUserEventRepository struct {
	items            []domain.RateUserEvent
	dailySummaries   []domain.RateUserEventDailySummary
	err              error
	lastNStatuses    []domain.RateUserEventStatus
	bySourceStatuses []domain.RateUserEventStatus
}

func (m *mockRateUserEventRepository) ObtainLastNRateUserEvents(_ context.Context, _, _ int64, s ...domain.RateUserEventStatus) ([]domain.RateUserEvent, error) {
	m.lastNStatuses = s
	return m.items, m.err
}

func (m *mockRateUserEventRepository) ObtainRateUserEventsBySourceName(_ context.Context, _ string, _, _ int64, s ...domain.RateUserEventStatus) ([]domain.RateUserEvent, error) {
	m.bySourceStatuses = s
	return m.items, m.err
}

func (m *mockRateUserEventRepository) ObtainDailyEventSummaryBySource(_ context.Context, _ string, _, _ int64) ([]domain.RateUserEventDailySummary, error) {
	return m.dailySummaries, m.err
}
