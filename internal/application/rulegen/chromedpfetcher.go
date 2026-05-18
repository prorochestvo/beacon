package rulegen

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// ChromedpFetcher renders pages with a headless Chrome instance and returns
// the post-hydration outer HTML of the <html> element. It implements the
// rulegen.Fetcher interface.
//
// Lifecycle: each Fetch call spawns a fresh browser. This is intentional for
// the cmd/rulegen single-source-per-invocation use case. When cmd/collector
// integrates the fetcher (future plan), reuse a long-lived allocator via a
// browser pool.
type ChromedpFetcher struct {
	timeout       time.Duration
	chromiumPath  string
	networkIdleMs int
	waitSelector  string // empty means "wait for body only"
	logger        io.Writer
}

// ChromedpFetcherOptions carries construction parameters for ChromedpFetcher.
// Zero values for numeric fields default to the package constants below.
type ChromedpFetcherOptions struct {
	// Timeout is the hard wall-clock deadline per Fetch call. Defaults to 30 s.
	Timeout time.Duration
	// ChromiumPath is the absolute path to the Chromium/Chrome binary. When
	// empty, chromedp falls back to its own PATH lookup order: chromium,
	// chromium-browser, google-chrome, chrome.
	ChromiumPath string
	// NetworkIdleMillis is the additional wait after body is visible before
	// capturing outerHTML. Defaults to 5000 ms — bumped from 1500 ms in plan 014
	// after live smoke-tests showed bank SPAs need 3–5 s post-body for the
	// rate table to hydrate.
	NetworkIdleMillis int
	// WaitSelector, if non-empty, replaces the default body+sleep wait
	// strategy with WaitVisible(selector). Use this for SPAs where the
	// rate table appears only after JS hydration. Empty falls back to
	// WaitVisible("body") + NetworkIdleMillis sleep.
	WaitSelector string
	// Logger receives one-line diagnostic messages per fetch. Nil defaults to
	// io.Discard (best-effort; errors writing to logger are not returned).
	Logger io.Writer
}

const (
	defaultChromedpTimeout       = 30 * time.Second
	defaultChromedpNetworkIdleMs = 5000
)

// NewChromedpFetcher constructs a ChromedpFetcher with the given options, applying
// defaults for any zero-valued numeric fields.
func NewChromedpFetcher(opts ChromedpFetcherOptions) *ChromedpFetcher {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultChromedpTimeout
	}
	if opts.NetworkIdleMillis <= 0 {
		opts.NetworkIdleMillis = defaultChromedpNetworkIdleMs
	}
	if opts.Logger == nil {
		opts.Logger = io.Discard
	}
	return &ChromedpFetcher{
		timeout:       opts.Timeout,
		chromiumPath:  opts.ChromiumPath,
		networkIdleMs: opts.NetworkIdleMillis,
		waitSelector:  strings.TrimSpace(opts.WaitSelector),
		logger:        opts.Logger,
	}
}

// networkIdleMillisForTest exposes the internal networkIdleMs field for
// assertions in package tests. It is intentionally unexported.
func (f *ChromedpFetcher) networkIdleMillisForTest() int {
	return f.networkIdleMs
}

// Fetch navigates to url with a headless Chrome instance and returns the
// post-hydration outer HTML of the document. The hard wall-clock timeout is
// enforced via context.WithTimeout independently of any chromedp internal
// per-action timeout.
//
// Both the browser context and the allocator context are cancelled before
// return so the Chromium subprocess is reaped even on error paths.
func (f *ChromedpFetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancelCtx := context.WithTimeout(ctx, f.timeout)
	defer cancelCtx()

	allocatorOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Headless,
		chromedp.DisableGPU,
		// NoSandbox is required when Chrome runs as root (systemd unit on the
		// ARM deploy host) or inside a container. Without it Chrome aborts at
		// startup with "Running as root without --no-sandbox is not supported."
		chromedp.NoSandbox,
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
	)
	if f.chromiumPath != "" {
		allocatorOpts = append(allocatorOpts, chromedp.ExecPath(f.chromiumPath))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocatorOpts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	actions := []chromedp.Action{
		chromedp.Navigate(url),
		chromedp.WaitVisible("body", chromedp.ByQuery),
	}
	if f.waitSelector != "" {
		actions = append(actions, chromedp.WaitVisible(f.waitSelector, chromedp.ByQuery))
	} else {
		actions = append(actions, chromedp.Sleep(time.Duration(f.networkIdleMs)*time.Millisecond))
	}
	var rendered string
	actions = append(actions, chromedp.OuterHTML("html", &rendered, chromedp.ByQuery))

	if err := chromedp.Run(browserCtx, actions...); err != nil {
		return nil, fmt.Errorf("chromedp fetch %s: %w", url, err)
	}

	// Logger writes are best-effort; a failure here must not suppress the
	// successfully fetched body.
	fmt.Fprintf(f.logger, "chromedp: url=%s wait_selector=%q rendered_bytes=%d\n",
		url, f.waitSelector, len(rendered))

	return []byte(rendered), nil
}
