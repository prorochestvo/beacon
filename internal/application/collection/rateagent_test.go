package collection

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/repository"
	"github.com/stretchr/testify/require"
)

var _ executionHistoryRepository = &repository.ExecutionHistoryRepository{}
var _ rateSourceRepository = &repository.RateSourceRepository{}
var _ rateValueRepository = &repository.RateValueRepository{}
var _ rateExtractor = &mockRateExtractor{}
var _ chromedpBatchExtractor = &mockRateExtractor{}

func TestNewRateAgent(t *testing.T) {
	t.Parallel()

	t.Run("valid construction", func(t *testing.T) {
		t.Parallel()

		agent, err := NewRateAgent(
			"",
			"",
			&mockRateSourceRepository{},
			&mockExecutionHistoryRepository{},
			&mockRateValueRepository{},
			io.Discard,
		)
		require.NoError(t, err)
		require.NotNil(t, agent)
	})
}

func TestRateAgent_execution(t *testing.T) {
	t.Parallel()

	t.Run("extractor succeeds — history retained as success", func(t *testing.T) {
		t.Parallel()

		histRepo := &mockExecutionHistoryRepository{}
		a := &RateAgent{
			plainExtractor:             &mockRateExtractor{},
			executionHistoryRepository: histRepo,
		}

		errs := a.execution(t.Context(), []domain.RateSource{{Name: "src1"}})
		require.Empty(t, errs)
		require.Len(t, histRepo.retained, 1)
		require.True(t, histRepo.retained[0].Success)
		require.Empty(t, histRepo.retained[0].Error)
		require.False(t, histRepo.retained[0].Timestamp.IsZero())
	})
	t.Run("extractor fails — history retained as failure", func(t *testing.T) {
		t.Parallel()

		histRepo := &mockExecutionHistoryRepository{}
		a := &RateAgent{
			plainExtractor:             &mockRateExtractor{err: errors.New("fetch error")},
			executionHistoryRepository: histRepo,
		}

		errs := a.execution(t.Context(), []domain.RateSource{{Name: "src1"}})
		require.Contains(t, errs, "src1", "failed source must appear in the returned error map")
		require.ErrorContains(t, errs["src1"], "fetch error")
		require.Len(t, histRepo.retained, 1)
		require.False(t, histRepo.retained[0].Success)
		require.NotEmpty(t, histRepo.retained[0].Error)
	})
	t.Run("failing source appears in returned error map", func(t *testing.T) {
		t.Parallel()

		a := &RateAgent{
			plainExtractor:             &mockRateExtractor{err: errors.New("fetch error")},
			executionHistoryRepository: &mockExecutionHistoryRepository{},
		}

		errs := a.execution(t.Context(), []domain.RateSource{{Name: "src1"}})
		require.NotNil(t, errs["src1"])
	})
	t.Run("multiple sources each get their own history record", func(t *testing.T) {
		t.Parallel()

		histRepo := &mockExecutionHistoryRepository{}
		a := &RateAgent{
			plainExtractor:             &mockRateExtractor{},
			executionHistoryRepository: histRepo,
		}

		errs := a.execution(t.Context(), []domain.RateSource{{Name: "src1"}, {Name: "src2"}})
		require.Empty(t, errs)
		require.Len(t, histRepo.retained, 2)
	})
}

func TestRateAgent_execution_dispatchesByFetcherKind(t *testing.T) {
	t.Parallel()

	t.Run("plain", func(t *testing.T) {
		t.Parallel()

		plain := &mockRateExtractor{}
		chrome := &mockRateExtractor{}
		a := &RateAgent{
			plainExtractor:             plain,
			chromedpExtractor:          chrome,
			executionHistoryRepository: &mockExecutionHistoryRepository{},
		}

		errs := a.execution(t.Context(), []domain.RateSource{{Name: "src1", FetcherKind: "plain"}})

		require.Empty(t, errs)
		require.Equal(t, 1, plain.calls, "plain extractor must be called for fetcher_kind=plain")
		require.Equal(t, 0, chrome.calls, "chromedp extractor must not be called for fetcher_kind=plain")
	})
	t.Run("empty fetcher_kind defaults to plain", func(t *testing.T) {
		t.Parallel()

		plain := &mockRateExtractor{}
		chrome := &mockRateExtractor{}
		a := &RateAgent{
			plainExtractor:             plain,
			chromedpExtractor:          chrome,
			executionHistoryRepository: &mockExecutionHistoryRepository{},
		}

		errs := a.execution(t.Context(), []domain.RateSource{{Name: "src1", FetcherKind: ""}})

		require.Empty(t, errs)
		require.Equal(t, 1, plain.calls, "plain extractor must be called for empty fetcher_kind")
		require.Equal(t, 0, chrome.calls, "chromedp extractor must not be called for empty fetcher_kind")
	})
	t.Run("chromedp", func(t *testing.T) {
		t.Parallel()

		plain := &mockRateExtractor{}
		chrome := &mockRateExtractor{}
		a := &RateAgent{
			plainExtractor:             plain,
			chromedpExtractor:          chrome,
			executionHistoryRepository: &mockExecutionHistoryRepository{},
		}

		errs := a.execution(t.Context(), []domain.RateSource{{Name: "src1", FetcherKind: "chromedp"}})

		require.Empty(t, errs)
		require.Equal(t, 0, plain.calls, "plain extractor must not be called for fetcher_kind=chromedp")
		require.Equal(t, 1, chrome.calls, "chromedp extractor must be called for fetcher_kind=chromedp")
	})
	t.Run("unsupported kind returns error", func(t *testing.T) {
		t.Parallel()

		plain := &mockRateExtractor{}
		chrome := &mockRateExtractor{}
		a := &RateAgent{
			plainExtractor:             plain,
			chromedpExtractor:          chrome,
			executionHistoryRepository: &mockExecutionHistoryRepository{},
		}

		errs := a.execution(t.Context(), []domain.RateSource{{Name: "badsrc", FetcherKind: "bogus"}})

		require.NotNil(t, errs["badsrc"], "unsupported fetcher_kind must produce an error entry")
		require.ErrorContains(t, errs["badsrc"], "unsupported fetcher_kind")
		require.ErrorContains(t, errs["badsrc"], "bogus")
		require.Equal(t, 0, plain.calls, "plain extractor must not be called for unsupported kind")
		require.Equal(t, 0, chrome.calls, "chromedp extractor must not be called for unsupported kind")
	})
}

func TestRateAgent_Run(t *testing.T) {
	t.Parallel()

	t.Run("no sources — returns nil", func(t *testing.T) {
		t.Parallel()

		a := &RateAgent{
			rateSourceRepository:       &mockRateSourceRepository{sources: nil},
			executionHistoryRepository: &mockExecutionHistoryRepository{},
			rateValueRepository:        &mockRateValueRepository{},
			plainExtractor:             &mockRateExtractor{},
			logger:                     io.Discard,
		}

		require.NoError(t, a.Run(t.Context()))
	})
	t.Run("source not due — extractor never called", func(t *testing.T) {
		t.Parallel()

		extractor := &mockRateExtractor{}
		histRepo := &mockExecutionHistoryRepository{
			records: []domain.ExecutionHistory{{
				SourceName: "src1",
				Success:    true,
				Timestamp:  time.Now().UTC(),
			}},
		}
		a := &RateAgent{
			rateSourceRepository:       &mockRateSourceRepository{sources: []domain.RateSource{{Name: "src1", Interval: "1h", Active: true}}},
			executionHistoryRepository: histRepo,
			rateValueRepository:        &mockRateValueRepository{},
			plainExtractor:             extractor,
			logger:                     io.Discard,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 0, extractor.calls)
	})
	t.Run("source due — execution runs and history is retained", func(t *testing.T) {
		t.Parallel()

		histRepo := &mockExecutionHistoryRepository{records: nil}
		a := &RateAgent{
			rateSourceRepository:       &mockRateSourceRepository{sources: []domain.RateSource{{Name: "src1", Interval: "1m", Title: "SRC", Active: true}}},
			executionHistoryRepository: histRepo,
			rateValueRepository:        &mockRateValueRepository{values: []domain.RateValue{{Price: 100}}},
			plainExtractor:             &mockRateExtractor{},
			logger:                     io.Discard,
		}

		require.NoError(t, a.Run(t.Context()))
		require.NotEmpty(t, histRepo.retained)
	})
	t.Run("invalid interval — error returned", func(t *testing.T) {
		t.Parallel()

		a := &RateAgent{
			rateSourceRepository:       &mockRateSourceRepository{sources: []domain.RateSource{{Name: "src1", Interval: "bad", Active: true}}},
			executionHistoryRepository: &mockExecutionHistoryRepository{},
			rateValueRepository:        &mockRateValueRepository{},
			plainExtractor:             &mockRateExtractor{},
			logger:                     io.Discard,
		}

		require.Error(t, a.Run(t.Context()))
	})
	t.Run("extractor error surfaced", func(t *testing.T) {
		t.Parallel()

		a := &RateAgent{
			rateSourceRepository:       &mockRateSourceRepository{sources: []domain.RateSource{{Name: "src1", Interval: "1m", Active: true}}},
			executionHistoryRepository: &mockExecutionHistoryRepository{},
			rateValueRepository:        &mockRateValueRepository{},
			plainExtractor:             &mockRateExtractor{err: errors.New("fetch error")},
			logger:                     io.Discard,
		}

		require.Error(t, a.Run(t.Context()))
	})
	t.Run("skips inactive source", func(t *testing.T) {
		t.Parallel()

		extractor := &mockRateExtractor{}
		a := &RateAgent{
			rateSourceRepository:       &mockRateSourceRepository{sources: []domain.RateSource{{Name: "src1", Interval: "1m", Active: false}}},
			executionHistoryRepository: &mockExecutionHistoryRepository{},
			rateValueRepository:        &mockRateValueRepository{},
			plainExtractor:             extractor,
			logger:                     io.Discard,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 0, extractor.calls, "inactive source must never be processed")
	})
	t.Run("source repo returns error", func(t *testing.T) {
		t.Parallel()

		a := &RateAgent{
			rateSourceRepository:       &mockRateSourceRepository{err: errors.New("db fail")},
			executionHistoryRepository: &mockExecutionHistoryRepository{},
			rateValueRepository:        &mockRateValueRepository{},
			plainExtractor:             &mockRateExtractor{},
			logger:                     io.Discard,
		}

		require.Error(t, a.Run(t.Context()))
	})
	t.Run("history repo error treats source as due", func(t *testing.T) {
		t.Parallel()

		extractor := &mockRateExtractor{}
		// obtainErr set → ObtainLastNExecutionHistoryBySourceName returns error →
		// isDue returns true → source is treated as due and extractor is called.
		histRepo := &mockExecutionHistoryRepository{obtainErr: errors.New("hist fail")}
		a := &RateAgent{
			rateSourceRepository:       &mockRateSourceRepository{sources: []domain.RateSource{{Name: "src1", Interval: "1h", Active: true}}},
			executionHistoryRepository: histRepo,
			rateValueRepository:        &mockRateValueRepository{},
			plainExtractor:             extractor,
			logger:                     io.Discard,
		}

		require.NoError(t, a.Run(t.Context()))
		require.Equal(t, 1, extractor.calls, "source must be treated as due when history fetch fails")
	})
}

type mockRateSourceRepository struct {
	sources []domain.RateSource
	err     error
}

func (m *mockRateSourceRepository) ObtainRateSourceByName(_ context.Context, _ string) (*domain.RateSource, error) {
	return nil, m.err
}

func (m *mockRateSourceRepository) ObtainAllRateSources(_ context.Context) ([]domain.RateSource, error) {
	return m.sources, m.err
}

type mockExecutionHistoryRepository struct {
	records   []domain.ExecutionHistory
	retained  []*domain.ExecutionHistory
	err       error
	obtainErr error
}

func (m *mockExecutionHistoryRepository) RetainExecutionHistory(_ context.Context, h *domain.ExecutionHistory) error {
	cp := *h
	m.retained = append(m.retained, &cp)
	return m.err
}

func (m *mockExecutionHistoryRepository) ObtainLastNExecutionHistoryBySourceName(_ context.Context, _ string, _ int64, _ bool) ([]domain.ExecutionHistory, error) {
	if m.obtainErr != nil {
		return nil, m.obtainErr
	}
	return m.records, m.err
}

type mockRateValueRepository struct {
	values []domain.RateValue
	err    error
}

func (m *mockRateValueRepository) ObtainLastNRateValuesBySourceName(_ context.Context, _ string, _ int64) ([]domain.RateValue, error) {
	return m.values, m.err
}

func (m *mockRateValueRepository) RetainRateValue(_ context.Context, _ *domain.RateValue) error {
	return m.err
}

type mockRateExtractor struct {
	err   error
	calls int
}

func (m *mockRateExtractor) Run(_ context.Context, _ *domain.RateSource) error {
	m.calls++
	return m.err
}

// RunBatch lets the same mock satisfy chromedpBatchExtractor. Each batch source
// counts as one call so per-source assertions (chrome.calls == 1 for one
// chromedp source) still hold.
func (m *mockRateExtractor) RunBatch(_ context.Context, batch []*domain.RateSource) map[string]error {
	out := make(map[string]error, len(batch))
	for _, s := range batch {
		m.calls++
		out[s.Name] = m.err
	}
	return out
}
