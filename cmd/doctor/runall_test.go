package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/application/rulegen"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ rateSourceLister = (*fakeLister)(nil)
var _ ruleGenerator = (*fakeGenerator)(nil)
var _ rulegen.Fetcher = (*sourceAuditFetcherAdapter)(nil)

// The production satisfiers passed to runAll.
var _ rateSourceLister = (*repository.RateSourceRepository)(nil)
var _ ruleGenerator = (*rulegen.Generator)(nil)

type fakeLister struct {
	sources []domain.RateSource
	err     error
}

func (f *fakeLister) ObtainAllRateSources(_ context.Context) ([]domain.RateSource, error) {
	return f.sources, f.err
}

type fakeGenerator struct {
	results map[string]*rulegen.Result
	errs    map[string]error
	panics  map[string]string
}

func (f *fakeGenerator) Generate(_ context.Context, sourceName string, _ bool) (*rulegen.Result, error) {
	if msg, ok := f.panics[sourceName]; ok {
		panic(msg)
	}
	if err, ok := f.errs[sourceName]; ok {
		return nil, err
	}
	if res, ok := f.results[sourceName]; ok {
		return res, nil
	}
	return &rulegen.Result{}, nil
}

func TestRunAll(t *testing.T) {
	t.Parallel()

	t.Run("all sources succeed", func(t *testing.T) {
		t.Parallel()

		lister := &fakeLister{
			sources: []domain.RateSource{
				{Name: "src-a", Active: true},
				{Name: "src-b", Active: true},
			},
		}
		gen := &fakeGenerator{
			results: map[string]*rulegen.Result{
				"src-a": {Rules: make([]domain.RateSourceRule, 2), Value: 1.5, AttemptsUsed: 1},
				"src-b": {Rules: make([]domain.RateSourceRule, 3), Value: 2.0, AttemptsUsed: 2},
			},
		}

		var out, errOut bytes.Buffer
		code := runAll(t.Context(), gen, lister, false, &out, &errOut)

		assert.Equal(t, 0, code)
		assert.Contains(t, out.String(), "OK source=src-a")
		assert.Contains(t, out.String(), "OK source=src-b")
		assert.Contains(t, out.String(), "rulegen --all: processed=2 succeeded=2 failed=0 skipped=0")
	})

	t.Run("one source fails, run still returns 0", func(t *testing.T) {
		t.Parallel()

		lister := &fakeLister{
			sources: []domain.RateSource{
				{Name: "src-ok", Active: true},
				{Name: "src-bad", Active: true},
			},
		}
		gen := &fakeGenerator{
			results: map[string]*rulegen.Result{
				"src-ok": {Value: 1.0, AttemptsUsed: 1},
			},
			errs: map[string]error{
				"src-bad": errors.New("attempts exhausted"),
			},
		}

		var out, errOut bytes.Buffer
		code := runAll(t.Context(), gen, lister, false, &out, &errOut)

		assert.Equal(t, 0, code)
		assert.Contains(t, out.String(), "OK source=src-ok")
		assert.Contains(t, out.String(), "FAIL source=src-bad")
		assert.Contains(t, out.String(), "rulegen --all: processed=2 succeeded=1 failed=1 skipped=0")
	})

	t.Run("panic in one source is recovered", func(t *testing.T) {
		t.Parallel()

		lister := &fakeLister{
			sources: []domain.RateSource{
				{Name: "src-panic", Active: true},
				{Name: "src-ok", Active: true},
			},
		}
		gen := &fakeGenerator{
			panics: map[string]string{
				"src-panic": "unexpected nil pointer",
			},
			results: map[string]*rulegen.Result{
				"src-ok": {Value: 1.0, AttemptsUsed: 1},
			},
		}

		var out, errOut bytes.Buffer
		require.NotPanics(t, func() {
			code := runAll(t.Context(), gen, lister, false, &out, &errOut)
			assert.Equal(t, 0, code)
		})

		assert.Contains(t, out.String(), "FAIL source=src-panic reason=panic")
		assert.Contains(t, out.String(), "unexpected nil pointer")
		assert.Contains(t, out.String(), "OK source=src-ok")
		assert.Contains(t, out.String(), "rulegen --all: processed=2 succeeded=1 failed=1 skipped=0")
	})

	t.Run("inactive sources are skipped", func(t *testing.T) {
		t.Parallel()

		lister := &fakeLister{
			sources: []domain.RateSource{
				{Name: "src-active", Active: true},
				{Name: "src-inactive", Active: false},
			},
		}
		gen := &fakeGenerator{
			results: map[string]*rulegen.Result{
				"src-active": {Value: 1.0, AttemptsUsed: 1},
			},
		}

		var out, errOut bytes.Buffer
		code := runAll(t.Context(), gen, lister, false, &out, &errOut)

		assert.Equal(t, 0, code)
		assert.Contains(t, out.String(), "OK source=src-active")
		assert.Contains(t, out.String(), fmt.Sprintf("SKIP source=%s reason=inactive", "src-inactive"))
		assert.Contains(t, out.String(), "rulegen --all: processed=1 succeeded=1 failed=0 skipped=1")
	})

	t.Run("lister error still returns 0", func(t *testing.T) {
		t.Parallel()

		lister := &fakeLister{err: errors.New("db connection lost")}
		gen := &fakeGenerator{}

		var out, errOut bytes.Buffer
		code := runAll(t.Context(), gen, lister, false, &out, &errOut)

		assert.Equal(t, 0, code)
		assert.Contains(t, errOut.String(), "FAIL mode=--all reason=list sources")
		assert.NotContains(t, out.String(), "FAIL", "FAIL line must not appear on stdout")
		assert.Contains(t, out.String(), "rulegen --all: processed=0 succeeded=0 failed=0 skipped=0")
	})

	t.Run("empty source list returns 0 with zero counters", func(t *testing.T) {
		t.Parallel()

		lister := &fakeLister{sources: []domain.RateSource{}}
		gen := &fakeGenerator{}

		var out, errOut bytes.Buffer
		code := runAll(t.Context(), gen, lister, false, &out, &errOut)

		assert.Equal(t, 0, code)
		assert.Empty(t, errOut.String())
		assert.NotContains(t, out.String(), "FAIL")
		assert.NotContains(t, out.String(), "OK")
		assert.NotContains(t, out.String(), "SKIP")
		assert.Contains(t, out.String(), "rulegen --all: processed=0 succeeded=0 failed=0 skipped=0")
	})
}
