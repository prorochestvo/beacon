package rateextractor

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestChromedpRateExtractor_failFast(t *testing.T) {
	t.Parallel()

	t.Run("recordFailedURL then loadFailedURL returns true", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("", "", io.Discard, &mockRateValueRepository{})

		sentinel := errors.New("sentinel error")
		e.recordFailedURL("https://example.com/rates", sentinel)

		got, ok := e.loadFailedURL("https://example.com/rates")
		require.True(t, ok, "loadFailedURL must return true for a recorded URL")
		require.Equal(t, sentinel, got, "loadFailedURL must return the stored error")
	})

	t.Run("loadFailedURL on unrecorded URL returns false", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("", "", io.Discard, &mockRateValueRepository{})

		got, ok := e.loadFailedURL("https://not-recorded.example.com/")
		require.False(t, ok, "loadFailedURL must return false for an unrecorded URL")
		require.Nil(t, got)
	})

	t.Run("recordFailedURL is concurrency-safe", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("", "", io.Discard, &mockRateValueRepository{})

		const workers = 20
		const concurrentURL = "https://example.com/concurrent"
		sentinel := errors.New("concurrent sentinel")

		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				e.recordFailedURL(concurrentURL, sentinel)
				_, _ = e.loadFailedURL(concurrentURL)
			}()
		}
		wg.Wait()

		_, ok := e.loadFailedURL(concurrentURL)
		require.True(t, ok, "URL must be recorded after concurrent writes")
	})

	t.Run("fetchRenderedPageInAllocator short-circuits after prior failure", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("", "", io.Discard, &mockRateValueRepository{})

		prior := errors.New("prior network error")
		e.recordFailedURL("https://example.com/rates", prior)

		src := &domain.RateSource{Name: "src", URL: "https://example.com/rates"}
		payload, err := e.fetchRenderedPageInAllocator(t.Context(), src)

		require.Nil(t, payload)
		require.Error(t, err)
		require.ErrorContains(t, err, "short-circuit")
		require.ErrorContains(t, err, "tombstoned this run")
		require.ErrorIs(t, err, prior)
	})
}

func TestChromedpRateExtractor_RunBatch(t *testing.T) {
	t.Parallel()

	t.Run("empty batch is a no-op and does not launch Chromium", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("", "", io.Discard, &mockRateValueRepository{})
		out := e.RunBatch(t.Context(), nil)
		require.Nil(t, out)
	})

	t.Run("cancelled parent ctx tags every source without launching Chromium", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("", "", io.Discard, &mockRateValueRepository{})

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		batch := []*domain.RateSource{
			{Name: "src1", URL: "https://example.com/a"},
			{Name: "src2", URL: "https://example.com/b"},
		}
		out := e.RunBatch(ctx, batch)

		require.Len(t, out, 2)
		require.ErrorIs(t, out["src1"], context.Canceled)
		require.ErrorIs(t, out["src2"], context.Canceled)
		require.ErrorContains(t, out["src1"], "src1")
		require.ErrorContains(t, out["src2"], "src2")
	})

	t.Run("tombstoned URL in batch short-circuits to the cached error", func(t *testing.T) {
		t.Parallel()

		// Live ctx + single tombstoned source. NewExecAllocator is lazy (the
		// subprocess spawns on first NewContext/Run), and the tombstone
		// short-circuit fires inside fetchRenderedPageInAllocator before any
		// NewContext, so the path stays hermetic without a real Chromium binary.
		e := NewChromedpRateExtractor("", "", io.Discard, &mockRateValueRepository{})
		prior := errors.New("prior network error")
		e.recordFailedURL("https://example.com/dead", prior)

		batch := []*domain.RateSource{
			{Name: "dead", URL: "https://example.com/dead"},
		}
		out := e.RunBatch(t.Context(), batch)

		require.Len(t, out, 1)
		require.ErrorIs(t, out["dead"], prior, "tombstoned URL must surface the cached error")
		require.ErrorContains(t, out["dead"], "short-circuit")
		require.ErrorContains(t, out["dead"], "tombstoned this run")
	})
}

func TestBuildExecAllocatorOptions(t *testing.T) {
	t.Parallel()

	// baseline is the number of options appended unconditionally.
	// len(chromedp.DefaultExecAllocatorOptions) + fixedExecAllocatorOptionCount
	// (Headless, DisableGPU, NoSandbox, disable-blink-features flag).
	baseline := len(chromedp.DefaultExecAllocatorOptions) + fixedExecAllocatorOptionCount

	t.Run("empty proxyURL appends no proxy option", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("", "", io.Discard, &mockRateValueRepository{})
		opts := e.buildExecAllocatorOptions("")

		require.Len(t, opts, baseline)
	})

	t.Run("non-empty proxyURL appends exactly one ProxyServer option", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("", "http://127.0.0.1:7788", io.Discard, &mockRateValueRepository{})
		opts := e.buildExecAllocatorOptions("http://127.0.0.1:7788")

		require.Len(t, opts, baseline+1)
	})

	t.Run("chromiumPath appends ExecPath option on top of proxy", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("/usr/bin/chromium", "http://127.0.0.1:7788", io.Discard, &mockRateValueRepository{})
		opts := e.buildExecAllocatorOptions("http://127.0.0.1:7788")

		// baseline + ProxyServer + ExecPath = baseline + 2
		require.Len(t, opts, baseline+2)
	})

	t.Run("chromiumPath without proxy appends only ExecPath", func(t *testing.T) {
		t.Parallel()

		e := NewChromedpRateExtractor("/usr/bin/chromium", "", io.Discard, &mockRateValueRepository{})
		opts := e.buildExecAllocatorOptions("")

		require.Len(t, opts, baseline+1)
	})
}
