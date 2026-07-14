package rulegen

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
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
		proxyURL:      opts.ProxyURL,
		networkIdleMs: opts.NetworkIdleMillis,
		waitSelector:  strings.TrimSpace(opts.WaitSelector),
		logger:        opts.Logger,
	}
}

// ChromedpFetcher renders pages with headless Chrome and returns the
// post-hydration outer HTML of the <html> element. Implements rulegen.Fetcher.
//
// Lifecycle: each Fetch call spawns a fresh browser, intended for the
// cmd/doctor rulegen single-source-per-invocation use case. When cmd/collector
// integrates the fetcher (future plan), reuse a long-lived allocator via a
// browser pool.
type ChromedpFetcher struct {
	timeout       time.Duration
	chromiumPath  string
	proxyURL      string
	networkIdleMs int
	waitSelector  string // empty means "wait for body only"
	logger        io.Writer
}

// Fetch navigates to url with a headless Chrome instance and returns the
// post-hydration outer HTML of the document. The hard wall-clock timeout is
// enforced via context.WithTimeout independently of any chromedp internal
// per-action timeout.
//
// headers is accepted for interface compliance but ignored: chromedp manages
// its own request headers internally and there is no per-source header injection
// for the headless path.
//
// Both the browser context and the allocator context are cancelled before
// return so the Chromium subprocess is reaped even on error paths.
func (f *ChromedpFetcher) Fetch(ctx context.Context, url string, _ map[string]string) ([]byte, error) {
	ctx, cancelCtx := context.WithTimeout(ctx, f.timeout)
	defer cancelCtx()

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, f.buildExecAllocatorOptions(f.proxyURL)...)
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

// networkIdleMillisForTest exposes networkIdleMs for package-test assertions.
func (f *ChromedpFetcher) networkIdleMillisForTest() int {
	return f.networkIdleMs
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
	// ProxyURL is an optional HTTP proxy URL string (e.g. "http://127.0.0.1:7788").
	// When empty, Chromium runs without a proxy.
	ProxyURL string
	// NetworkIdleMillis is the additional wait after body is visible before
	// capturing outerHTML. Defaults to 5000 ms — bank SPAs need 3–5 s post-body
	// for the rate table to hydrate.
	NetworkIdleMillis int
	// WaitSelector, if non-empty, replaces the default body+sleep strategy with
	// WaitVisible(selector) for SPAs where the rate table appears only after JS
	// hydration. Empty falls back to WaitVisible("body") + NetworkIdleMillis sleep.
	WaitSelector string
	// Logger receives one-line diagnostic messages per fetch. Nil defaults to
	// io.Discard (best-effort; errors writing to logger are not returned).
	Logger io.Writer
}

const (
	defaultChromedpTimeout       = 30 * time.Second
	defaultChromedpNetworkIdleMs = 5000
)

// fixedExecAllocatorOptionCount is the count of options buildExecAllocatorOptions
// appends unconditionally (Headless, DisableGPU, NoSandbox, disable-blink-features).
// Tests use it to compute the baseline option count without a magic literal.
const fixedExecAllocatorOptionCount = 4

// buildExecAllocatorOptions builds the full chromedp allocator option slice.
// A non-empty proxyURL appends a ProxyServer option — Chromium does not inherit
// the Go proxy env, so the URL must be passed explicitly.
func (f *ChromedpFetcher) buildExecAllocatorOptions(proxyURL string) []chromedp.ExecAllocatorOption {
	// slices.Clone — never alias chromedp.DefaultExecAllocatorOptions; an
	// upstream array-length change would otherwise let append() stomp the global.
	opts := append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]),
		chromedp.Headless,
		chromedp.DisableGPU,
		// NoSandbox is required when Chrome runs as root (systemd unit on the
		// ARM deploy host) or inside a container. Without it Chrome aborts at
		// startup with "Running as root without --no-sandbox is not supported."
		chromedp.NoSandbox,
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
	)
	if proxyURL != "" {
		opts = append(opts, chromedp.ProxyServer(proxyURL))
	}
	if f.chromiumPath != "" {
		opts = append(opts, chromedp.ExecPath(f.chromiumPath))
	}
	return opts
}
