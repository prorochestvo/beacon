package rateextractor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
)

// ChromedpRateExtractor renders pages using a headless Chrome instance, then
// applies the source's extraction rule pipeline and persists the resulting rate
// value. RunBatch shares one Chromium subprocess across every source in the
// batch; Run delegates to RunBatch with a single-element slice.
//
// The constructor is lazy-friendly: pass an empty chromiumPath to let chromedp
// fall back to its own PATH lookup (chromium, chromium-browser, google-chrome, chrome).
type ChromedpRateExtractor struct {
	chromiumPath string
	proxyURL     string
	logger       io.Writer
	repo         rateValueRepository
	failedURLs   map[string]error
	failedURLsMu sync.Mutex
}

// NewChromedpRateExtractor constructs a ChromedpRateExtractor. Empty chromiumPath
// lets chromedp search PATH. proxyURL is an optional HTTP proxy URL (e.g.
// "http://127.0.0.1:7788"); "" runs Chromium without a proxy. logger receives a
// one-line diagnostic per fetch; pass io.Discard to silence. repo must be non-nil.
//
// The extractor keeps a per-process negative URL cache (tombstone): once a URL
// fails, later fetches in the same process short-circuit. Built for short-lived
// one-shot processes; do not reuse an instance across cron invocations in a daemon.
func NewChromedpRateExtractor(chromiumPath string, proxyURL string, logger io.Writer, repo rateValueRepository) *ChromedpRateExtractor {
	if logger == nil {
		logger = io.Discard
	}
	return &ChromedpRateExtractor{
		chromiumPath: chromiumPath,
		proxyURL:     proxyURL,
		logger:       logger,
		repo:         repo,
		failedURLs:   make(map[string]error),
	}
}

// Run renders source.URL via headless Chrome with a one-shot allocator and
// applies the extraction pipeline. Prefer RunBatch when processing multiple
// sources in one tick — it amortises Chromium cold-start across the batch.
//
// Errors from RunBatch are not re-wrapped: they already carry the
// "chromedp extractor: ..." / "chromedp fetch ..." prefix.
func (e *ChromedpRateExtractor) Run(ctx context.Context, source *domain.RateSource) error {
	return e.RunBatch(ctx, []*domain.RateSource{source})[source.Name]
}

// RunBatch fetches and persists every source under one shared Chromium
// subprocess. The result map is keyed by source.Name; a nil or absent entry
// signals success. An empty batch is a fast no-op — Chromium is never launched,
// so a tick with only plain sources pays zero chromedp cost.
//
// Sources are processed sequentially: browser contexts from one ExecAllocator
// share a single CDP websocket and are not safe to use concurrently. Each source
// gets a fresh chromedp.NewContext so cookies, storage, and per-target options
// do not leak between rates.
//
// The shared ExecAllocator (and its subprocess) is cancelled via defer before
// return, including on panic paths, so the subprocess is always reaped. If the
// parent ctx is cancelled mid-batch the remaining sources are tagged with
// ctx.Err() rather than silently dropped, so the caller can still record
// per-source execution history.
//
// Trust boundary. The subprocess runs with --no-sandbox (required when chromedp
// runs as root on the deploy host) and shares one --user-data-dir across the
// batch. That shared profile exposes the network service, DNS cache, HTTP disk
// cache, and HTTP credential cache to every source — chromedp.NewContext
// partitions cookies and local storage per target but not the network service.
// Populate batch only from operator-vetted, https-scheme, credential-free
// `rate_sources.url` rows; mixing user-supplied or credential-bearing URLs into
// one batch widens the blast radius of a renderer exploit or auth-cache leak
// across every source that follows.
func (e *ChromedpRateExtractor) RunBatch(ctx context.Context, batch []*domain.RateSource) map[string]error {
	if len(batch) == 0 {
		return nil
	}

	allocCtx, cancelAlloc := e.newExecAllocator(ctx)
	defer cancelAlloc()

	out := make(map[string]error, len(batch))
	for _, source := range batch {
		if err := ctx.Err(); err != nil {
			out[source.Name] = fmt.Errorf("chromedp extractor: source %s: %w", source.Name, err)
			continue
		}
		out[source.Name] = e.runOneInAllocator(allocCtx, source)
	}
	return out
}

// newExecAllocator builds the shared chromedp allocator used by RunBatch.
// Contexts derived from allocCtx share one Chromium network service: DNS, HTTP
// disk, and HTTP auth credential caches are process-wide. Cookies and local
// storage are partitioned per chromedp.NewContext, but the network layer is not.
// See the RunBatch trust-boundary section for what may be batched together.
// Caller owns the returned cancel func.
func (e *ChromedpRateExtractor) newExecAllocator(ctx context.Context) (context.Context, context.CancelFunc) {
	return chromedp.NewExecAllocator(ctx, e.buildExecAllocatorOptions(e.proxyURL)...)
}

// fixedExecAllocatorOptionCount is the number of options buildExecAllocatorOptions
// appends unconditionally (Headless, DisableGPU, NoSandbox, disable-blink-features).
// Tests use it to compute the baseline option count without a magic literal.
const fixedExecAllocatorOptionCount = 4

// buildExecAllocatorOptions constructs the full slice of chromedp allocator
// options. A non-empty proxyURL appends a ProxyServer option; Chromium does not
// inherit the Go proxy env, so the URL must be passed explicitly.
func (e *ChromedpRateExtractor) buildExecAllocatorOptions(proxyURL string) []chromedp.ExecAllocatorOption {
	// slices.Clone — never alias chromedp.DefaultExecAllocatorOptions; an
	// upstream array-length change would otherwise let append() stomp the global.
	opts := append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]),
		chromedp.Headless,
		chromedp.DisableGPU,
		// NoSandbox is required when Chrome runs as root (systemd unit on the
		// ARM deploy host) or inside a container.
		chromedp.NoSandbox,
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
	)
	if proxyURL != "" {
		opts = append(opts, chromedp.ProxyServer(proxyURL))
	}
	if e.chromiumPath != "" {
		opts = append(opts, chromedp.ExecPath(e.chromiumPath))
	}
	return opts
}

// runOneInAllocator fetches one source under an existing allocator and applies
// the extraction pipeline. The per-source 30 s timeout is scoped inside
// fetchRenderedPageInAllocator so a slow source cannot starve the rest of the batch.
//
// applyRulesAndStore runs under allocCtx (batch lifetime), not the per-source
// timeout: rendering is done, the payload is in memory, and the DB write must
// not be cut off by the 30 s navigation deadline. allocCtx has no deadline of
// its own — it carries whatever RunBatch's caller passed in.
func (e *ChromedpRateExtractor) runOneInAllocator(allocCtx context.Context, source *domain.RateSource) error {
	payload, err := e.fetchRenderedPageInAllocator(allocCtx, source)
	if err != nil {
		return err
	}
	return applyRulesAndStore(allocCtx, source, payload, e.repo)
}

// fetchRenderedPageInAllocator navigates to source.URL inside the supplied
// allocator and returns the post-hydration outer HTML. The per-source
// wall-clock timeout (defaultChromedpTimeout) is applied locally so its expiry
// affects only this source, not the batch's other sources or the subprocess.
// Short-circuits if a prior fetch for source.URL failed this process lifetime;
// see recordFailedURL.
func (e *ChromedpRateExtractor) fetchRenderedPageInAllocator(allocCtx context.Context, source *domain.RateSource) ([]byte, error) {
	if err := validateNavigableURL(source.URL); err != nil {
		wrapped := fmt.Errorf("chromedp extractor: source %s: %w", source.Name, err)
		// Tombstone so later ticks short-circuit without re-validating/re-logging —
		// a malformed `rate_sources.url` would otherwise flood logs.
		e.recordFailedURL(source.URL, wrapped)
		return nil, wrapped
	}

	if cached, ok := e.loadFailedURL(source.URL); ok {
		_, _ = fmt.Fprintf(e.logger,
			"chromedp_extractor: short-circuit url=%s prior_error=%v\n", source.URL, cached)
		err := fmt.Errorf("short-circuit (tombstoned this run): %w", cached)
		err = errors.Join(err, internal.NewTraceError())
		return nil, err
	}

	sourceCtx, cancelSource := context.WithTimeout(allocCtx, defaultChromedpTimeout)
	defer cancelSource()

	browserCtx, cancelBrowser := chromedp.NewContext(sourceCtx)
	defer cancelBrowser()

	waitSelector := strings.TrimSpace(source.Options.WaitSelector)

	actions := []chromedp.Action{
		chromedp.Navigate(source.URL),
		chromedp.WaitVisible("body", chromedp.ByQuery),
	}
	if waitSelector != "" {
		actions = append(actions, chromedp.WaitVisible(waitSelector, chromedp.ByQuery))
	} else {
		actions = append(actions, chromedp.Sleep(time.Duration(defaultChromedpNetworkIdleMs)*time.Millisecond))
	}
	var rendered string
	actions = append(actions, chromedp.OuterHTML("html", &rendered, chromedp.ByQuery))

	if err := chromedp.Run(browserCtx, actions...); err != nil {
		wrapped := fmt.Errorf("chromedp fetch %s: %w", source.URL, err)
		e.recordFailedURL(source.URL, wrapped)
		return nil, wrapped
	}

	_, _ = fmt.Fprintf(e.logger, "chromedp_extractor: url=%s wait_selector=%q rendered_bytes=%d\n",
		source.URL, waitSelector, len(rendered))

	return []byte(rendered), nil
}

// loadFailedURL returns the cached error for url and true if url was previously
// recorded as failed during the current process lifetime.
func (e *ChromedpRateExtractor) loadFailedURL(url string) (error, bool) {
	e.failedURLsMu.Lock()
	defer e.failedURLsMu.Unlock()
	err, ok := e.failedURLs[url]
	return err, ok
}

// recordFailedURL stores err as the tombstone for url. Subsequent fetches of url
// inside the same process short-circuit and return a wrapped form of err at replay time.
// See constructor godoc for lifetime constraint.
func (e *ChromedpRateExtractor) recordFailedURL(url string, err error) {
	e.failedURLsMu.Lock()
	defer e.failedURLsMu.Unlock()
	e.failedURLs[url] = err
}

const (
	defaultChromedpTimeout       = 30 * time.Second
	defaultChromedpNetworkIdleMs = 5000
)

// validateNavigableURL rejects URLs Navigate must never touch — empty,
// malformed, or non-http(s) (file://, javascript:, data:, ...). The source URL
// comes from the database; an operator with write access could otherwise turn a
// chromedp source into a local-file read or SSRF.
func validateNavigableURL(rawURL string) error {
	if rawURL == "" {
		return errors.New("source URL must not be empty")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return fmt.Errorf("URL %q has no host", rawURL)
		}
		// Reject userinfo so credentials in `rate_sources.url` never reach the
		// navigate call or the error/log message that quotes the URL on failure.
		if u.User != nil {
			return fmt.Errorf("URL must not contain userinfo (credentials in URLs are not allowed)")
		}
		return nil
	default:
		return fmt.Errorf("URL scheme %q is not allowed (only http/https)", u.Scheme)
	}
}
