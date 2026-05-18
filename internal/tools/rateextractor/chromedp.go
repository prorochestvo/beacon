package rateextractor

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/seilbekskindirov/monitor/internal/domain"
)

const (
	defaultChromedpTimeout       = 30 * time.Second
	defaultChromedpNetworkIdleMs = 5000
)

// ChromedpRateExtractor renders pages using a headless Chrome instance, then
// applies the source's extraction rule pipeline and persists the resulting rate
// value. Each Run call spawns a fresh Chromium subprocess; callers that need
// high-throughput collection should consider a future browser-pool plan.
//
// The constructor is lazy-friendly: pass an empty chromiumPath to let chromedp
// fall back to its own PATH lookup (chromium, chromium-browser, google-chrome, chrome).
type ChromedpRateExtractor struct {
	chromiumPath string
	logger       io.Writer
	repo         rateValueRepository
}

// NewChromedpRateExtractor constructs a ChromedpRateExtractor. chromiumPath may
// be empty, in which case chromedp searches PATH for a suitable binary. logger
// receives one-line diagnostic messages per fetch; pass io.Discard to silence them.
// Caller must supply a non-nil repo.
func NewChromedpRateExtractor(chromiumPath string, logger io.Writer, repo rateValueRepository) *ChromedpRateExtractor {
	if logger == nil {
		logger = io.Discard
	}
	return &ChromedpRateExtractor{
		chromiumPath: chromiumPath,
		logger:       logger,
		repo:         repo,
	}
}

// Run renders source.URL via headless Chrome, applies all extraction rules in
// sequence, and persists the resulting rate value via the repository supplied at
// construction time. The WaitSelector from source.Options is honoured per call
// so different sources with different selectors share one extractor instance.
func (e *ChromedpRateExtractor) Run(ctx context.Context, source *domain.RateSource) error {
	payload, err := e.fetchRenderedPage(ctx, source)
	if err != nil {
		return fmt.Errorf("chromedp extractor: source %s: %w", source.Name, err)
	}
	return applyRulesAndStore(ctx, source, payload, e.repo)
}

// fetchRenderedPage navigates to source.URL with a headless Chrome instance and
// returns the post-hydration outer HTML of the document. The hard wall-clock timeout
// is 30 s. Both the browser context and the allocator context are cancelled before
// return so the Chromium subprocess is reaped even on error paths.
func (e *ChromedpRateExtractor) fetchRenderedPage(ctx context.Context, source *domain.RateSource) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultChromedpTimeout)
	defer cancel()

	allocatorOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Headless,
		chromedp.DisableGPU,
		// NoSandbox is required when Chrome runs as root (systemd unit on the
		// ARM deploy host) or inside a container.
		chromedp.NoSandbox,
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
	)
	if e.chromiumPath != "" {
		allocatorOpts = append(allocatorOpts, chromedp.ExecPath(e.chromiumPath))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocatorOpts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
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
		return nil, fmt.Errorf("chromedp fetch %s: %w", source.URL, err)
	}

	_, _ = fmt.Fprintf(e.logger, "chromedp_extractor: url=%s wait_selector=%q rendered_bytes=%d\n",
		source.URL, waitSelector, len(rendered))

	return []byte(rendered), nil
}
