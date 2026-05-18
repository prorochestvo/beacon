package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seilbekskindirov/monitor/internal/application/rulegen"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ rulegenGenerator = (*fakeGenerator)(nil)
	_ rulegenGenerator = (*blockingGenerator)(nil)
)

const (
	adminID    = int64(999)
	nonAdminID = int64(111)
)

// fakeGenerator is a test double for rulegenGenerator.
type fakeGenerator struct {
	result        *rulegen.Result
	err           error
	blockUntilCtx bool // when true, blocks until ctx is cancelled before returning
	callCount     int
	lastSource    string
	lastForce     bool
	mu            sync.Mutex
}

func (f *fakeGenerator) Generate(ctx context.Context, sourceName string, forceFallback bool) (*rulegen.Result, error) {
	f.mu.Lock()
	f.callCount++
	f.lastSource = sourceName
	f.lastForce = forceFallback
	f.mu.Unlock()

	if f.blockUntilCtx {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.result, f.err
}

// fakeGeneratorFactory is a test double for rulegenGeneratorFactory that
// records calls and returns a configured fakeGenerator.
type fakeGeneratorFactory struct {
	gen            rulegenGenerator
	err            error
	calledPrimary  int
	calledFallback int
	mu             sync.Mutex
}

func (f *fakeGeneratorFactory) build(maxPrimary, maxFallback int) (rulegenGenerator, error) {
	f.mu.Lock()
	f.calledPrimary = maxPrimary
	f.calledFallback = maxFallback
	f.mu.Unlock()
	return f.gen, f.err
}

// cannedResult returns a minimal valid Result for use in happy-path tests.
func cannedResult(sourceName string) *rulegen.Result {
	src := &domain.RateSource{
		Name:          sourceName,
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
	}
	return &rulegen.Result{
		Source: src,
		Rules: []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(\d+\.\d+)`},
		},
		Metadata: domain.RateSourceRuleMetadata{
			Provider:     "OpenAI",
			Model:        "gpt-4o",
			AttemptsUsed: 1,
			GeneratedAt:  "2026-05-17T08:30:00Z",
		},
		Value:        467.95,
		AttemptsUsed: 1,
		Escalated:    false,
	}
}

// newRulegenHandler builds a Handler wired for GenerateRules tests.
// defaultGen and factory may be nil when the test does not reach that path.
func newRulegenHandler(t *testing.T, defaultGen rulegenGenerator, factory rulegenGeneratorFactory, admin int64) *Handler {
	t.Helper()
	h, err := NewHandler(&mockRateService{}, "bot-token", &mockMeSubRepo{}, &mockMeSourceRepo{}, &mockMeRateValueRepo{},
		defaultGen, factory, admin, rulegen.NewLockManager())
	require.NoError(t, err)
	// Replace validator with a fake; each test sub-case sets its own.
	h.validateInitData = alwaysValidateInitData(admin)
	return h
}

// doPost fires a POST to /api/sources/{name}/rules/generate against the handler.
func doPost(t *testing.T, h *Handler, sourceName string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/sources/"+sourceName+"/rules/generate", &buf)
	req = req.WithContext(t.Context())
	req.SetPathValue("name", sourceName)
	req.Header.Set("X-Telegram-Init-Data", "valid")
	rr := httptest.NewRecorder()
	h.GenerateRules(rr, req)
	return rr
}

func TestHandler_GenerateRules(t *testing.T) {
	t.Parallel()

	t.Run("missing path name returns 400", func(t *testing.T) {
		t.Parallel()
		h := newRulegenHandler(t, nil, nil, adminID)
		req := httptest.NewRequest(http.MethodPost, "/api/sources//rules/generate", nil)
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GenerateRules(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "missing source name")
	})

	t.Run("missing initData returns 401", func(t *testing.T) {
		t.Parallel()
		h := newRulegenHandler(t, nil, nil, adminID)
		h.validateInitData = alwaysRejectInitData

		req := httptest.NewRequest(http.MethodPost, "/api/sources/my-source/rules/generate", nil)
		req.SetPathValue("name", "my-source")
		rr := httptest.NewRecorder()
		h.GenerateRules(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		assert.Contains(t, rr.Body.String(), "unauthorized")
	})

	t.Run("non-admin user returns 403", func(t *testing.T) {
		t.Parallel()
		h := newRulegenHandler(t, nil, nil, adminID)
		h.validateInitData = alwaysValidateInitData(nonAdminID) // authenticated but NOT admin

		req := httptest.NewRequest(http.MethodPost, "/api/sources/my-source/rules/generate", nil)
		req.SetPathValue("name", "my-source")
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GenerateRules(rr, req)
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), "admin-only")
	})

	t.Run("malformed JSON body returns 400", func(t *testing.T) {
		t.Parallel()
		h := newRulegenHandler(t, nil, nil, adminID)
		req := httptest.NewRequest(http.MethodPost, "/api/sources/my-source/rules/generate",
			strings.NewReader("{not valid json"))
		req.SetPathValue("name", "my-source")
		req.Header.Set("X-Telegram-Init-Data", "valid")
		rr := httptest.NewRecorder()
		h.GenerateRules(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "invalid request body")
	})

	t.Run("out-of-range max_primary_attempts returns 400", func(t *testing.T) {
		t.Parallel()
		h := newRulegenHandler(t, nil, nil, adminID)
		rr := doPost(t, h, "my-source", api.RulegenRequest{MaxPrimaryAttempts: 11})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "max_primary_attempts")
	})

	t.Run("out-of-range max_fallback_attempts returns 400", func(t *testing.T) {
		t.Parallel()
		h := newRulegenHandler(t, nil, nil, adminID)
		rr := doPost(t, h, "my-source", api.RulegenRequest{MaxFallbackAttempts: -1})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "max_fallback_attempts")
	})

	t.Run("unknown source returns 404", func(t *testing.T) {
		t.Parallel()
		gen := &fakeGenerator{err: fmt.Errorf("wrap: %w", rulegen.ErrSourceNotFound)}
		h := newRulegenHandler(t, gen, nil, adminID)
		rr := doPost(t, h, "ghost-source", nil)
		assert.Equal(t, http.StatusNotFound, rr.Code)
		assert.Contains(t, rr.Body.String(), "source not found")
	})

	t.Run("unsupported fetcher kind returns 503", func(t *testing.T) {
		t.Parallel()
		gen := &fakeGenerator{err: fmt.Errorf("wrap: %w", rulegen.ErrUnsupportedFetcherKind)}
		h := newRulegenHandler(t, gen, nil, adminID)
		rr := doPost(t, h, "chromedp-source", nil)
		assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
		assert.Contains(t, rr.Body.String(), "required fetcher is not available")
	})

	t.Run("attempts exhausted returns 502", func(t *testing.T) {
		t.Parallel()
		gen := &fakeGenerator{err: fmt.Errorf("wrap: %w", rulegen.ErrAttemptsExhausted)}
		h := newRulegenHandler(t, gen, nil, adminID)
		rr := doPost(t, h, "my-source", nil)
		assert.Equal(t, http.StatusBadGateway, rr.Code)
		var body map[string]string
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body),
			"502 response body must be valid JSON, not Go-quoted-string concatenation")
		assert.Contains(t, body["error"], "rule generation failed for source")
		assert.Contains(t, body["error"], "my-source")
	})

	// context deadline test is NOT parallel because it mutates the package-level
	// rulegenRequestTimeout var. Running it sequentially prevents a data race with
	// other subtests that read the var concurrently.
	t.Run("context deadline returns 504", func(t *testing.T) {
		gen := &fakeGenerator{blockUntilCtx: true}
		h := newRulegenHandler(t, gen, nil, adminID)

		orig := rulegenRequestTimeout
		rulegenRequestTimeout = 5 * time.Millisecond
		t.Cleanup(func() { rulegenRequestTimeout = orig })

		rr := doPost(t, h, "my-source", nil)
		assert.Equal(t, http.StatusGatewayTimeout, rr.Code)
		assert.Contains(t, rr.Body.String(), "timed out")
	})

	t.Run("lock contention returns 409", func(t *testing.T) {
		t.Parallel()
		// First goroutine holds the lock throughout; second call must see 409.
		blocker := make(chan struct{})
		release := make(chan struct{})
		done := make(chan struct{})

		// The first request's generator blocks until released, then returns an error
		// (ErrAttemptsExhausted) so we can assert a non-panicking HTTP response for it.
		blocking := &blockingGenerator{
			inner:   &fakeGenerator{err: fmt.Errorf("wrap: %w", rulegen.ErrAttemptsExhausted)},
			blocker: blocker,
			release: release,
		}

		h := newRulegenHandler(t, blocking, nil, adminID)

		req1 := httptest.NewRequest(http.MethodPost, "/api/sources/hot-source/rules/generate", nil)
		req1.SetPathValue("name", "hot-source")
		req1.Header.Set("X-Telegram-Init-Data", "valid")
		req1 = req1.WithContext(t.Context())
		rr1 := httptest.NewRecorder()

		go func() {
			defer close(done)
			h.GenerateRules(rr1, req1)
		}()

		// Wait until the first goroutine is inside Generate and holding the lock.
		<-blocker

		// Second request must see 409 because the lock is still held.
		req2 := httptest.NewRequest(http.MethodPost, "/api/sources/hot-source/rules/generate", nil)
		req2.SetPathValue("name", "hot-source")
		req2.Header.Set("X-Telegram-Init-Data", "valid")
		req2 = req2.WithContext(t.Context())
		rr2 := httptest.NewRecorder()
		h.GenerateRules(rr2, req2)
		assert.Equal(t, http.StatusConflict, rr2.Code)
		assert.Contains(t, rr2.Body.String(), "already in progress")

		// Release the first goroutine and wait for it to finish.
		release <- struct{}{}
		<-done
		assert.Equal(t, http.StatusBadGateway, rr1.Code)
	})

	t.Run("happy path returns 200 with persisted shape", func(t *testing.T) {
		t.Parallel()
		result := cannedResult("my-source")
		gen := &fakeGenerator{result: result}
		h := newRulegenHandler(t, gen, nil, adminID)

		rr := doPost(t, h, "my-source", nil)
		require.Equal(t, http.StatusOK, rr.Code)

		var body api.RulegenResponse
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		assert.Equal(t, "my-source", body.Source)
		assert.InDelta(t, 467.95, body.Value, 0.001)
		assert.Len(t, body.Rules, 1)
		assert.Equal(t, 1, body.AttemptsUsed)
		assert.False(t, body.Escalated)
		assert.Equal(t, "OpenAI", body.Provider)
		assert.Equal(t, "gpt-4o", body.Model)
		assert.Equal(t, "2026-05-17T08:30:00Z", body.GeneratedAt)
	})

	t.Run("override attempt budget invokes factory", func(t *testing.T) {
		t.Parallel()
		result := cannedResult("my-source")
		factory := &fakeGeneratorFactory{gen: &fakeGenerator{result: result}}
		h := newRulegenHandler(t, nil, factory.build, adminID)

		rr := doPost(t, h, "my-source", api.RulegenRequest{MaxPrimaryAttempts: 5, MaxFallbackAttempts: 1})
		require.Equal(t, http.StatusOK, rr.Code)

		factory.mu.Lock()
		defer factory.mu.Unlock()
		assert.Equal(t, 5, factory.calledPrimary, "factory must receive overridden maxPrimary")
		assert.Equal(t, 1, factory.calledFallback, "factory must receive overridden maxFallback")
	})

	t.Run("force_fallback true passed to generator", func(t *testing.T) {
		t.Parallel()
		result := cannedResult("my-source")
		gen := &fakeGenerator{result: result}
		h := newRulegenHandler(t, gen, nil, adminID)

		rr := doPost(t, h, "my-source", api.RulegenRequest{ForceFallback: true})
		require.Equal(t, http.StatusOK, rr.Code)
		assert.True(t, gen.lastForce)
	})
}

// blockingGenerator is a test double that signals the caller when it has
// entered Generate and then blocks until the caller sends on release.
type blockingGenerator struct {
	inner   rulegenGenerator
	blocker chan struct{} // closed when Generate is entered; caller waits on this
	release chan struct{} // caller sends to unblock Generate
}

func (b *blockingGenerator) Generate(ctx context.Context, sourceName string, forceFallback bool) (*rulegen.Result, error) {
	close(b.blocker) // signal that we're inside Generate and hold the handler's lock
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.inner.Generate(ctx, sourceName, forceFallback)
}
