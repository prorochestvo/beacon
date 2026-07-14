package rulegen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/artificialintelligence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ artificialintelligence.AIClient = (*mockAIClient)(nil)
var _ rateSourceRepository = (*mockSourceRepo)(nil)
var _ Fetcher = (*mockFetcher)(nil)
var _ RuleExecutor = (*mockRuleExecutor)(nil)
var _ RuleExecutor = (*sequentialExecutor)(nil)

type mockAIClient struct {
	name      string
	model     string
	responses []string
	callCount int
	err       error
}

func (m *mockAIClient) Name() string  { return m.name }
func (m *mockAIClient) Model() string { return m.model }

func (m *mockAIClient) CheckUP(_ context.Context) error { return nil }

func (m *mockAIClient) Complete(_ context.Context, _, _ string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if m.callCount >= len(m.responses) {
		return "", errors.New("mockAIClient: no more responses configured")
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

type mockSourceRepo struct {
	source      *domain.RateSource
	findErr     error
	retainErr   error
	retainCount int
}

func (m *mockSourceRepo) ObtainRateSourceByName(_ context.Context, _ string) (*domain.RateSource, error) {
	return m.source, m.findErr
}

func (m *mockSourceRepo) RetainRateSource(_ context.Context, _ *domain.RateSource) error {
	m.retainCount++
	return m.retainErr
}

type mockFetcher struct {
	body []byte
	err  error
}

func (m *mockFetcher) Fetch(_ context.Context, _ string, _ map[string]string) ([]byte, error) {
	return m.body, m.err
}

type mockRuleExecutor struct {
	value float64
	err   error
}

func (m *mockRuleExecutor) Execute(_ []domain.RateSourceRule, _ []byte, _, _ string) (float64, error) {
	return m.value, m.err
}

func newTestSource() *domain.RateSource {
	return &domain.RateSource{
		Name:          "test-source",
		Title:         "Test Source",
		URL:           "https://example.com/rates",
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
		Kind:          domain.RateSourceKindASK,
		Active:        true,
		Rules:         []domain.RateSourceRule{},
	}
}

const validRulesJSON = `{"rules":[{"method":"regex","pattern":"(\\d+\\.\\d+)"}]}`

// newTestGenerator wraps NewGenerator for tests. plain is the plain HTTP
// fetcher slot; chromedpFor is the chromedp factory slot. Either may be nil to
// simulate a build without that fetcher. Pass
// func(string) Fetcher { return mockFetcher } to wrap a mock.
func newTestGenerator(
	t *testing.T,
	primary, fallback *mockAIClient,
	plain Fetcher,
	chromedpFor func(string) Fetcher,
	executor RuleExecutor,
	repo *mockSourceRepo,
) *Generator {
	t.Helper()
	gen, err := NewGenerator(primary, fallback, plain, chromedpFor, executor, repo, 3, 2, io.Discard)
	require.NoError(t, err)
	return gen
}

func TestGenerator_Generate(t *testing.T) {
	t.Parallel()

	t.Run("primary succeeds on first attempt", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI", responses: []string{validRulesJSON}}
		fallback := &mockAIClient{name: "FallbackAI"}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: []byte("rate: 450.00")}
		executor := &mockRuleExecutor{value: 450.00}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		result, err := gen.Generate(t.Context(), "test-source", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 1, result.AttemptsUsed)
		assert.False(t, result.Escalated)
		assert.InDelta(t, 450.00, result.Value, 0.0001)
		assert.Equal(t, 1, repo.retainCount)
		assert.Equal(t, 1, primary.callCount)
		assert.Equal(t, 0, fallback.callCount)
	})

	t.Run("primary succeeds on third attempt after two failures", func(t *testing.T) {
		t.Parallel()
		// Attempt 1: parse fails (no executor call)
		// Attempt 2: parse succeeds, executor returns error
		// Attempt 3: parse succeeds, executor returns success
		primary := &mockAIClient{
			name: "PrimaryAI",
			responses: []string{
				`not json at all`,
				`{"rules":[{"method":"regex","pattern":"(nomatch)"}]}`,
				validRulesJSON,
			},
		}
		fallback := &mockAIClient{name: "FallbackAI"}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: []byte("rate: 450.00")}
		executorFn := &sequentialExecutor{
			results: []execResult{
				{0, errors.New("regex did not match")},
				{450.00, nil},
			},
		}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executorFn, repo)

		result, err := gen.Generate(t.Context(), "test-source", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 3, result.AttemptsUsed)
		assert.False(t, result.Escalated)
		assert.Equal(t, 1, repo.retainCount)
		assert.Equal(t, 3, primary.callCount)
	})

	t.Run("primary exhausted fallback succeeds", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{
			name:      "PrimaryAI",
			responses: []string{`not json`, `not json`, `not json`},
		}
		fallback := &mockAIClient{name: "FallbackAI", responses: []string{validRulesJSON}}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: []byte("rate: 450.00")}
		executor := &mockRuleExecutor{value: 450.00}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		result, err := gen.Generate(t.Context(), "test-source", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 4, result.AttemptsUsed)
		assert.True(t, result.Escalated)
		assert.Equal(t, 1, repo.retainCount)
		assert.Equal(t, 3, primary.callCount)
		assert.Equal(t, 1, fallback.callCount)
	})

	t.Run("primary exhausted fallback also fails", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{
			name:      "PrimaryAI",
			responses: []string{`not json`, `not json`, `not json`},
		}
		fallback := &mockAIClient{
			name:      "FallbackAI",
			responses: []string{`not json either`, `not json either again`},
		}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: []byte("rate: 450.00")}
		executor := &mockRuleExecutor{value: 0, err: errors.New("no match")}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		result, err := gen.Generate(t.Context(), "test-source", false)
		require.Error(t, err)
		require.Nil(t, result)
		assert.Equal(t, 0, repo.retainCount)
		assert.Contains(t, err.Error(), "all attempts exhausted")
		assert.Contains(t, err.Error(), "primary=3, fallback=2")
		assert.True(t, errors.Is(err, ErrAttemptsExhausted), "expected ErrAttemptsExhausted sentinel")
		assert.Equal(t, 2, fallback.callCount)
	})

	t.Run("primary exhausted fallback succeeds on second attempt", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{
			name:      "PrimaryAI",
			responses: []string{`not json`, `not json`, `not json`},
		}
		fallback := &mockAIClient{
			name:      "FallbackAI",
			responses: []string{`not json`, validRulesJSON},
		}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: []byte("rate: 450.00")}
		executor := &mockRuleExecutor{value: 450.00}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		result, err := gen.Generate(t.Context(), "test-source", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 5, result.AttemptsUsed)
		assert.True(t, result.Escalated)
		assert.Equal(t, 1, repo.retainCount)
		assert.Equal(t, 3, primary.callCount)
		assert.Equal(t, 2, fallback.callCount)
	})

	t.Run("force fallback skips primary", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI"}
		fallback := &mockAIClient{name: "FallbackAI", responses: []string{validRulesJSON}}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: []byte("rate: 450.00")}
		executor := &mockRuleExecutor{value: 450.00}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		result, err := gen.Generate(t.Context(), "test-source", true)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 0, primary.callCount)
		assert.Equal(t, 1, fallback.callCount)
		assert.True(t, result.Escalated)
		assert.Equal(t, 1, result.AttemptsUsed)
	})

	t.Run("unknown source name returns error", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI"}
		fallback := &mockAIClient{name: "FallbackAI"}
		repo := &mockSourceRepo{source: nil}
		fetcher := &mockFetcher{body: []byte("ignored")}
		executor := &mockRuleExecutor{}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		result, err := gen.Generate(t.Context(), "nonexistent", false)
		require.Error(t, err)
		require.Nil(t, result)
		assert.Contains(t, err.Error(), "not found")
		assert.True(t, errors.Is(err, ErrSourceNotFound), "expected ErrSourceNotFound sentinel")
		assert.Equal(t, 0, primary.callCount)
	})

	t.Run("returns ErrSourceNotFound for unknown source", func(t *testing.T) {
		t.Parallel()
		repo := &mockSourceRepo{source: nil}
		gen := newTestGenerator(t,
			&mockAIClient{name: "P"},
			&mockAIClient{name: "F"},
			&mockFetcher{body: []byte("x")},
			nil,
			&mockRuleExecutor{},
			repo,
		)
		_, err := gen.Generate(t.Context(), "missing-source", false)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrSourceNotFound))
	})

	t.Run("returns ErrAttemptsExhausted when all attempts fail", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{
			name:      "PrimaryAI",
			responses: []string{"bad", "bad", "bad"},
		}
		fallback := &mockAIClient{
			name:      "FallbackAI",
			responses: []string{"bad", "bad"},
		}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: []byte("rate: 450.00")}
		executor := &mockRuleExecutor{}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		_, err := gen.Generate(t.Context(), "test-source", false)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrAttemptsExhausted))
	})

	t.Run("body exceeds 5 MB returns error before any AI call", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI"}
		fallback := &mockAIClient{name: "FallbackAI"}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: make([]byte, maxRawBodyBytes+1)}
		executor := &mockRuleExecutor{}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		result, err := gen.Generate(t.Context(), "test-source", false)
		require.Error(t, err)
		require.Nil(t, result)
		assert.Contains(t, err.Error(), "5 MB")
		assert.Equal(t, 0, primary.callCount)
		assert.Equal(t, 0, fallback.callCount)
	})

	t.Run("primary attempt with invalid regex retries with transcript feedback", func(t *testing.T) {
		t.Parallel()
		// Attempt 1: lookbehind rule (RE2-illegal). Attempt 2: valid JSON.
		// validate catches the bad pattern before Execute runs, so the executor
		// is never called for attempt 1; outcome is AttemptsUsed=2 with one
		// successful persist.
		const invalidRegexJSON = `{"rules":[{"method":"regex","pattern":"(?<=USD)\\d+"}]}`
		primary := &mockAIClient{
			name:      "PrimaryAI",
			responses: []string{invalidRegexJSON, validRulesJSON},
		}
		fallback := &mockAIClient{name: "FallbackAI"}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: []byte("rate: 450.00")}
		// If attempt 1 had called the executor, the result would be an error,
		// not success.
		executor := &sequentialExecutor{
			results: []execResult{
				{450.00, nil},
			},
		}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		result, err := gen.Generate(t.Context(), "test-source", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 2, result.AttemptsUsed)
		assert.Equal(t, 2, primary.callCount)
		assert.Equal(t, 1, repo.retainCount)
		assert.Equal(t, 1, executor.idx, "executor must be called exactly once (attempt 2 only)")
	})

	t.Run("malformed JSON from primary counts as failure and proceeds to attempt 2", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{
			name:      "PrimaryAI",
			responses: []string{`not json`, validRulesJSON},
		}
		fallback := &mockAIClient{name: "FallbackAI"}
		repo := &mockSourceRepo{source: newTestSource()}
		fetcher := &mockFetcher{body: []byte("rate: 450.00")}
		executor := &mockRuleExecutor{value: 450.00}

		gen := newTestGenerator(t, primary, fallback, fetcher, nil, executor, repo)

		result, err := gen.Generate(t.Context(), "test-source", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 2, result.AttemptsUsed)
		assert.Equal(t, 2, primary.callCount)
	})

	t.Run("plausibility rejection retries and persists corrected value", func(t *testing.T) {
		t.Parallel()
		// Attempt 1: executor returns "outside plausible range" for USD/KZT.
		// Attempt 2: executor returns 470 (in range). The attempt-1 log must
		// contain "outside plausible range"; the persisted value must be 470.
		primary := &mockAIClient{
			name:      "PrimaryAI",
			responses: []string{validRulesJSON, validRulesJSON},
		}
		fallback := &mockAIClient{name: "FallbackAI"}
		src := newTestSource() // BaseCurrency=USD, QuoteCurrency=KZT
		repo := &mockSourceRepo{source: src}
		fetcher := &mockFetcher{body: []byte("rate: 19.1671")}
		executorFn := &sequentialExecutor{
			results: []execResult{
				{0, fmt.Errorf("rulegen: value 19.1671 rejected: outside plausible range [100, 1000] for USD/KZT")},
				{470.0, nil},
			},
		}

		var logBuf strings.Builder
		gen, err := NewGenerator(primary, fallback, fetcher, nil, executorFn, repo, 3, 2, &logBuf)
		require.NoError(t, err)

		result, err := gen.Generate(t.Context(), "test-source", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 2, result.AttemptsUsed)
		assert.InDelta(t, 470.0, result.Value, 0.0001)
		assert.Equal(t, 1, repo.retainCount)

		// The log must show the plausibility rejection on attempt 1.
		assert.Contains(t, logBuf.String(), "outside plausible range")
	})

	t.Run("chromedp source uses chromedp fetcher and persists", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI", responses: []string{validRulesJSON}}
		fallback := &mockAIClient{name: "FallbackAI"}
		repo := &mockSourceRepo{
			source: &domain.RateSource{
				Name:          "chromedp-src",
				Title:         "Chromedp Source",
				URL:           "https://example.com/spa",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Kind:          domain.RateSourceKindBID,
				Active:        true,
				FetcherKind:   "chromedp",
				Rules:         []domain.RateSourceRule{},
			},
		}
		// panicFetcher in the plain slot proves the chromedp fetcher, not the
		// plain one, is called when fetcher_kind="chromedp".
		chromedpFetcher := &mockFetcher{body: []byte("rate: 467.00")}
		executor := &mockRuleExecutor{value: 467.00}

		gen, err := NewGenerator(primary, fallback, &panicFetcher{},
			func(_ string) Fetcher { return chromedpFetcher },
			executor, repo, 3, 2, io.Discard)
		require.NoError(t, err)

		result, err := gen.Generate(t.Context(), "chromedp-src", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 1, result.AttemptsUsed)
		assert.Equal(t, 1, repo.retainCount)
		assert.InDelta(t, 467.00, result.Value, 0.0001)
	})

	t.Run("chromedp source with nil chromedp fetcher returns ErrUnsupportedFetcherKind", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI"}
		fallback := &mockAIClient{name: "FallbackAI"}
		repo := &mockSourceRepo{
			source: &domain.RateSource{
				Name:          "chromedp-missing",
				Title:         "Chromedp Missing",
				URL:           "https://example.com/spa",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Kind:          domain.RateSourceKindBID,
				Active:        true,
				FetcherKind:   "chromedp",
				Rules:         []domain.RateSourceRule{},
			},
		}
		// nil chromedp slot simulates a build without the chromedp fetcher;
		// panicFetcher in the plain slot proves the plain fetcher is not
		// called either.
		executor := &mockRuleExecutor{}

		gen, err := NewGenerator(primary, fallback, &panicFetcher{}, nil, executor, repo, 3, 2, io.Discard)
		require.NoError(t, err)

		_, err = gen.Generate(t.Context(), "chromedp-missing", false)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrUnsupportedFetcherKind),
			"expected ErrUnsupportedFetcherKind, got: %v", err)
		assert.Equal(t, 0, primary.callCount)
		assert.Equal(t, 0, fallback.callCount)
		assert.Equal(t, 0, repo.retainCount)
	})

	t.Run("chromedp source: wait_selector from options propagates to fetcher factory", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI", responses: []string{validRulesJSON}}
		fallback := &mockAIClient{name: "FallbackAI"}
		repo := &mockSourceRepo{
			source: &domain.RateSource{
				Name:          "chromedp-selector",
				Title:         "Chromedp Selector Source",
				URL:           "https://example.com/spa",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Kind:          domain.RateSourceKindBID,
				Active:        true,
				FetcherKind:   "chromedp",
				Options:       domain.RateSourceOptions{WaitSelector: "div.text-lg"},
				Rules:         []domain.RateSourceRule{},
			},
		}
		executor := &mockRuleExecutor{value: 467.00}

		var capturedSelector string
		chromedpFor := func(waitSelector string) Fetcher {
			capturedSelector = waitSelector
			return &mockFetcher{body: []byte("rate: 467.00")}
		}

		gen, err := NewGenerator(primary, fallback, &panicFetcher{}, chromedpFor, executor, repo, 3, 2, io.Discard)
		require.NoError(t, err)

		result, err := gen.Generate(t.Context(), "chromedp-selector", false)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "div.text-lg", capturedSelector,
			"Generator must pass options.WaitSelector to the chromedp factory")
		assert.InDelta(t, 467.00, result.Value, 0.0001)
	})
}

// panicFetcher implements Fetcher but panics if Fetch is called, verifying the
// guard short-circuits before any HTTP fetch.
type panicFetcher struct{}

var _ Fetcher = (*panicFetcher)(nil)

func (p *panicFetcher) Fetch(_ context.Context, url string, _ map[string]string) ([]byte, error) {
	panic("panicFetcher.Fetch called unexpectedly for URL: " + url)
}

func TestNewGenerator(t *testing.T) {
	t.Parallel()

	t.Run("nil primary returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewGenerator(nil, nil, &mockFetcher{}, nil, &mockRuleExecutor{}, &mockSourceRepo{}, 3, 2, io.Discard)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "primary")
	})

	t.Run("maxPrimary less than 1 returns error", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI"}
		_, err := NewGenerator(primary, nil, &mockFetcher{}, nil, &mockRuleExecutor{}, &mockSourceRepo{}, 0, 2, io.Discard)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "maxPrimaryAttempts")
	})

	t.Run("maxFallback less than 1 returns error", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI"}
		_, err := NewGenerator(primary, nil, &mockFetcher{}, nil, &mockRuleExecutor{}, &mockSourceRepo{}, 3, 0, io.Discard)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "maxFallbackAttempts")
	})

	t.Run("nil fallback gets stub substitute", func(t *testing.T) {
		t.Parallel()
		primary := &mockAIClient{name: "PrimaryAI"}
		gen, err := NewGenerator(primary, nil, &mockFetcher{}, nil, &mockRuleExecutor{}, &mockSourceRepo{}, 3, 2, io.Discard)
		require.NoError(t, err)
		require.NotNil(t, gen)
		assert.NotNil(t, gen.fallback)
	})
}

// sequentialExecutor returns results in sequence, one per call.
type sequentialExecutor struct {
	results []execResult
	idx     int
}

type execResult struct {
	value float64
	err   error
}

func (s *sequentialExecutor) Execute(_ []domain.RateSourceRule, _ []byte, _, _ string) (float64, error) {
	if s.idx >= len(s.results) {
		return 0, errors.New("sequentialExecutor: no more results")
	}
	r := s.results[s.idx]
	s.idx++
	return r.value, r.err
}

func TestValidateRulePatterns(t *testing.T) {
	t.Parallel()

	t.Run("valid regex rule passes", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(\d+\.\d+)`},
		}
		assert.NoError(t, validateRulePatterns(rules))
	})

	t.Run("json path rule is skipped", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodJSONPath, Pattern: "usd.sell"},
		}
		assert.NoError(t, validateRulePatterns(rules))
	})

	t.Run("lookbehind produces regex did not compile error", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(?<=USD)\d+`},
		}
		err := validateRulePatterns(rules)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "regex did not compile")
	})

	t.Run("backreference produces regex did not compile error", func(t *testing.T) {
		t.Parallel()
		rules := []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(\d+)\1`},
		}
		err := validateRulePatterns(rules)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "regex did not compile")
	})

	t.Run("empty rules slice passes", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, validateRulePatterns(nil))
	})
}

func TestParseRulesResponse(t *testing.T) {
	t.Parallel()

	t.Run("valid regex rule", func(t *testing.T) {
		t.Parallel()
		rules, err := parseRulesResponse(`{"rules":[{"method":"regex","pattern":"(\\d+)"}]}`)
		require.NoError(t, err)
		require.Len(t, rules, 1)
		assert.Equal(t, domain.MethodRegex, rules[0].Method)
		assert.Equal(t, `(\d+)`, rules[0].Pattern)
	})

	t.Run("valid json rule", func(t *testing.T) {
		t.Parallel()
		rules, err := parseRulesResponse(`{"rules":[{"method":"json","pattern":"usd.sell"}]}`)
		require.NoError(t, err)
		require.Len(t, rules, 1)
		assert.Equal(t, domain.MethodJSONPath, rules[0].Method)
	})

	t.Run("strips markdown code fences", func(t *testing.T) {
		t.Parallel()
		response := "```json\n" + `{"rules":[{"method":"regex","pattern":"(\\d+)"}]}` + "\n```"
		rules, err := parseRulesResponse(response)
		require.NoError(t, err)
		require.Len(t, rules, 1)
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		t.Parallel()
		_, err := parseRulesResponse("not json")
		require.Error(t, err)
	})

	t.Run("empty rules array returns error", func(t *testing.T) {
		t.Parallel()
		_, err := parseRulesResponse(`{"rules":[]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no rules")
	})

	t.Run("unknown method returns error", func(t *testing.T) {
		t.Parallel()
		_, err := parseRulesResponse(`{"rules":[{"method":"css","pattern":"#rate"}]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported method")
	})

	t.Run("empty pattern returns error", func(t *testing.T) {
		t.Parallel()
		_, err := parseRulesResponse(`{"rules":[{"method":"regex","pattern":""}]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pattern must not be empty")
	})
}

func TestGenerator_BuildUserMessage(t *testing.T) {
	t.Parallel()

	t.Run("message without transcript has no PREVIOUS ATTEMPTS section", func(t *testing.T) {
		t.Parallel()
		gen := &Generator{}
		src := newTestSource()
		msg := gen.buildUserMessage(src, []byte("body content"), 1024, nil)
		assert.Contains(t, msg, "SOURCE: test-source")
		assert.Contains(t, msg, "USD/KZT")
		assert.Contains(t, msg, "body content")
		assert.NotContains(t, msg, "PREVIOUS ATTEMPTS")
	})

	t.Run("message with transcript includes PREVIOUS ATTEMPTS section", func(t *testing.T) {
		t.Parallel()
		gen := &Generator{}
		src := newTestSource()
		transcript := []transcriptEntry{
			{Attempt: 1, Rule: `[{"method":"regex"}]`, Outcome: "error: regex did not match"},
		}
		msg := gen.buildUserMessage(src, []byte("body"), 2048, transcript)
		assert.Contains(t, msg, "PREVIOUS ATTEMPTS")
		assert.Contains(t, msg, "Attempt 1:")
		assert.Contains(t, msg, "regex did not match")
	})

	t.Run("options reserve hint is included", func(t *testing.T) {
		t.Parallel()
		gen := &Generator{}
		src := newTestSource()
		src.Options.Reserve = "pick the second USD row"
		msg := gen.buildUserMessage(src, []byte("body"), 100, nil)
		assert.Contains(t, msg, "pick the second USD row")
	})

	t.Run("missing options reserve shows none", func(t *testing.T) {
		t.Parallel()
		gen := &Generator{}
		src := newTestSource()
		msg := gen.buildUserMessage(src, []byte("body"), 100, nil)
		assert.Contains(t, msg, "HINT:   none")
	})
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	t.Run("short string is returned unchanged", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "hello", truncate("hello", 10))
	})

	t.Run("long string is truncated with ellipsis", func(t *testing.T) {
		t.Parallel()
		s := strings.Repeat("a", 600)
		result := truncate(s, 512)
		// "…" is 3 bytes UTF-8, so the total is 512 + 3 = 515 bytes.
		assert.True(t, len(result) <= 515, "truncated string should be at most 515 bytes (512 + 3-byte ellipsis)")
		assert.True(t, strings.HasSuffix(result, "…"))
	})
}

func TestValidateSourceURL(t *testing.T) {
	t.Parallel()

	t.Run("https URL is accepted", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, validateSourceURL("https://example.com/rates"))
	})

	t.Run("http URL is accepted", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, validateSourceURL("http://example.com/rates"))
	})

	t.Run("file scheme is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("file:///etc/passwd")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed")
	})

	t.Run("gopher scheme is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("gopher://example.com/")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed")
	})

	t.Run("javascript scheme is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("javascript:alert(1)")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed")
	})

	t.Run("empty string is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("scheme-only URL is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateSourceURL("ftp://")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not allowed")
	})
}
