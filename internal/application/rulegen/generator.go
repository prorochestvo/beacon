package rulegen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/infrastructure/artificialintelligence"
)

// ErrUnsupportedFetcherKind is returned by Generate when the source requires a
// fetcher that this build does not implement (e.g. "headless" / chromedp).
// Callers can distinguish this from a transient generation failure via
// errors.Is(err, ErrUnsupportedFetcherKind) and exit with a different code.
var ErrUnsupportedFetcherKind = errors.New("rulegen: unsupported fetcher kind")

// ErrSourceNotFound is returned when the named source is absent from the
// database. Callers use errors.Is to distinguish "no such source" from
// internal DB failures.
var ErrSourceNotFound = errors.New("rulegen: source not found")

// ErrAttemptsExhausted is returned when every primary and fallback attempt
// was made and none produced a rule that executed against the live body.
var ErrAttemptsExhausted = errors.New("rulegen: all attempts exhausted")

// Fetcher performs HTTP GETs and returns the raw response body.
// The rulegen package defines its own narrow interface rather than importing
// sourceaudit.Fetcher to avoid a cross-application dependency.
type Fetcher interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
}

// rateSourceRepository is the minimal persistence interface the generator needs.
type rateSourceRepository interface {
	ObtainRateSourceByName(ctx context.Context, name string) (*domain.RateSource, error)
	RetainRateSource(ctx context.Context, record *domain.RateSource) error
}

// Result holds the outcome of a successful Generate call.
type Result struct {
	// Source is the persisted rate source with its Rules and RuleMetadata updated.
	Source *domain.RateSource
	// Rules is the accepted rule chain that was persisted to the source.
	Rules []domain.RateSourceRule
	// Metadata records provenance information for the generated rules.
	Metadata domain.RateSourceRuleMetadata
	// Value is the rate extracted during generation, used to verify rule correctness.
	Value float64
	// AttemptsUsed is the total number of LLM calls consumed across primary and fallback.
	AttemptsUsed int
	// Escalated is true when the primary model was exhausted and the fallback model succeeded.
	Escalated bool
}

// transcriptEntry records one failed attempt for inclusion in the next prompt.
type transcriptEntry struct {
	Attempt int
	Rule    string // marshaled JSON of the proposed rule(s)
	Outcome string // human-readable failure reason
}

// Generator orchestrates the LLM audit loop that produces an extraction rule
// for a given rate source.
type Generator struct {
	primary            artificialintelligence.AIClient
	fallback           artificialintelligence.AIClient
	plainFetcher       Fetcher
	chromedpFetcherFor func(waitSelector string) Fetcher
	executor           RuleExecutor
	sourceRepo         rateSourceRepository
	maxPrimary         int
	maxFallback        int
	logger             io.Writer
}

// NewGenerator constructs a Generator. maxPrimaryAttempts must be >= 1 and
// maxFallbackAttempts must be >= 1. primary must not be nil. If fallback is nil,
// NewStubClient() is used so the loop logic is uniform regardless of fallback
// configuration.
//
// plainFetcher handles sources with fetcher_kind="" or "plain".
// chromedpFetcherFor is a factory that constructs a per-source ChromedpFetcher
// with the given waitSelector baked in. It handles sources with fetcher_kind="chromedp".
// Either slot may be nil; Generate returns ErrUnsupportedFetcherKind when the
// required slot is nil.
func NewGenerator(
	primary, fallback artificialintelligence.AIClient,
	plainFetcher Fetcher,
	chromedpFetcherFor func(waitSelector string) Fetcher,
	executor RuleExecutor,
	sourceRepo rateSourceRepository,
	maxPrimaryAttempts int,
	maxFallbackAttempts int,
	logger io.Writer,
) (*Generator, error) {
	if primary == nil {
		return nil, errors.New("rulegen: primary AI client must not be nil")
	}
	if maxPrimaryAttempts < 1 {
		return nil, fmt.Errorf("rulegen: maxPrimaryAttempts must be >= 1, got %d", maxPrimaryAttempts)
	}
	if maxFallbackAttempts < 1 {
		return nil, fmt.Errorf("rulegen: maxFallbackAttempts must be >= 1, got %d", maxFallbackAttempts)
	}
	if fallback == nil {
		stub, err := artificialintelligence.NewStubClient()
		if err != nil {
			return nil, fmt.Errorf("rulegen: build stub fallback: %w", err)
		}
		fallback = stub
	}
	return &Generator{
		primary:            primary,
		fallback:           fallback,
		plainFetcher:       plainFetcher,
		chromedpFetcherFor: chromedpFetcherFor,
		executor:           executor,
		sourceRepo:         sourceRepo,
		maxPrimary:         maxPrimaryAttempts,
		maxFallback:        maxFallbackAttempts,
		logger:             logger,
	}, nil
}

// Generate runs the audit loop for the named source. If forceFallback is true
// it skips primary and goes straight to one fallback attempt.
//
// On success the accepted rule and metadata are persisted to the source row
// and the Result is returned. On failure (all attempts exhausted) a non-nil
// error is returned and nothing is persisted.
func (g *Generator) Generate(ctx context.Context, sourceName string, forceFallback bool) (*Result, error) {
	src, err := g.sourceRepo.ObtainRateSourceByName(ctx, sourceName)
	if err != nil {
		return nil, fmt.Errorf("rulegen: load source %q: %w", sourceName, err)
	}
	if src == nil {
		return nil, fmt.Errorf("rulegen: source %q not found: %w", sourceName, ErrSourceNotFound)
	}

	// Select the fetcher based on the source's fetcher_kind.
	// "" and "plain" both route to the plain HTTP fetcher; "plain" is the
	// canonical value while "" is the defensive default for legacy rows.
	// "chromedp" routes to the headless Chrome fetcher.
	// Either slot may be nil when this build does not include the required
	// fetcher — Generate returns ErrUnsupportedFetcherKind so callers can
	// distinguish this from a transient generation failure and exit with code 2.
	var activeFetcher Fetcher
	switch src.FetcherKind {
	case "", "plain":
		if g.plainFetcher == nil {
			return nil, fmt.Errorf(
				"rulegen: source %q requires fetcher_kind=plain but no plain fetcher is configured (allowed: plain, chromedp): %w",
				sourceName, ErrUnsupportedFetcherKind,
			)
		}
		activeFetcher = g.plainFetcher
	case "chromedp":
		if g.chromedpFetcherFor == nil {
			return nil, fmt.Errorf(
				"rulegen: source %q requires fetcher_kind=chromedp but no chromedp fetcher is configured (allowed: plain, chromedp): %w",
				sourceName, ErrUnsupportedFetcherKind,
			)
		}
		activeFetcher = g.chromedpFetcherFor(src.Options.WaitSelector)
	default:
		return nil, fmt.Errorf(
			"rulegen: source %q has unknown fetcher_kind=%q (allowed: plain, chromedp): %w",
			sourceName, src.FetcherKind, ErrUnsupportedFetcherKind,
		)
	}

	rawBody, err := activeFetcher.Fetch(ctx, src.URL)
	if err != nil {
		return nil, fmt.Errorf("rulegen: fetch %s: %w", src.URL, err)
	}

	// The co-location guard in Locate requires a currency anchor within
	// ±defaultCoLocationBytes of a tier-1 hit, so anchors that appear in
	// marketing sections far from the rate table are automatically rejected.
	// <div class="text-lg is included because every seeded BCC rule uses it
	// as the structural marker immediately preceding the currency code and
	// rate value — the co-location guard ensures we pick the rate-table
	// occurrence, not a marketing heading.
	structural := []string{
		"<table", "<tbody", "<tr ",
		`<div class="text-lg`,
		`<div class="rate`,
		`class="currency"`,
		`class="exchange"`,
		`data-currency=`,
	}
	currency := []string{src.BaseCurrency, src.QuoteCurrency}
	body, originalSize, err := Sanitize(rawBody, structural, currency)
	if err != nil {
		return nil, err
	}

	_, _ = fmt.Fprintf(g.logger, "rulegen: source=%s url=%s body_original=%d body_stripped=%d\n",
		src.Name, src.URL, originalSize, len(body))

	var transcript []transcriptEntry
	attemptCount := 0

	if !forceFallback {
		for attempt := 1; attempt <= g.maxPrimary; attempt++ {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("rulegen: context cancelled before attempt %d: %w", attempt, err)
			}

			userMsg := g.buildUserMessage(src, body, originalSize, transcript)
			response, callErr := g.primary.Complete(ctx, systemPrompt, userMsg)
			attemptCount++

			if callErr != nil {
				entry := transcriptEntry{
					Attempt: attempt,
					Rule:    "(no response)",
					Outcome: fmt.Sprintf("error: %v", callErr),
				}
				transcript = append(transcript, entry)
				_, _ = fmt.Fprintf(g.logger, "rulegen: primary attempt %d failed (AI error): %v\n", attempt, callErr)
				continue
			}

			rules, parseErr := parseRulesResponse(response)
			if parseErr != nil {
				entry := transcriptEntry{
					Attempt: attempt,
					Rule:    truncate(response, 512),
					Outcome: fmt.Sprintf("error: %v", parseErr),
				}
				transcript = append(transcript, entry)
				_, _ = fmt.Fprintf(g.logger, "rulegen: primary attempt %d failed (parse error): %v\n", attempt, parseErr)
				continue
			}

			if validateErr := validateRulePatterns(rules); validateErr != nil {
				ruleJSON := marshalRules(rules)
				entry := transcriptEntry{
					Attempt: attempt,
					Rule:    truncate(ruleJSON, 512),
					Outcome: fmt.Sprintf("error: %v", validateErr),
				}
				transcript = append(transcript, entry)
				_, _ = fmt.Fprintf(g.logger, "rulegen: primary attempt %d failed (validate error): %v\n", attempt, validateErr)
				continue
			}

			value, execErr := g.executor.Execute(rules, body, src.BaseCurrency, src.QuoteCurrency)
			ruleJSON := marshalRules(rules)
			if execErr != nil {
				entry := transcriptEntry{
					Attempt: attempt,
					Rule:    truncate(ruleJSON, 512),
					Outcome: fmt.Sprintf("error: %v", execErr),
				}
				transcript = append(transcript, entry)
				_, _ = fmt.Fprintf(g.logger, "rulegen: primary attempt %d failed (exec error): %v\n", attempt, execErr)
				continue
			}

			return g.persist(ctx, src, rules, value, attemptCount, false, g.primary.Name(), g.primary.Model())
		}
	}

	for attempt := 1; attempt <= g.maxFallback; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("rulegen: context cancelled before fallback attempt %d: %w", attempt, err)
		}

		userMsg := g.buildUserMessage(src, body, originalSize, transcript)
		response, callErr := g.fallback.Complete(ctx, systemPrompt, userMsg)
		attemptCount++

		if callErr != nil {
			entry := transcriptEntry{
				Attempt: attemptCount,
				Rule:    "(no response)",
				Outcome: fmt.Sprintf("error: %v", callErr),
			}
			transcript = append(transcript, entry)
			_, _ = fmt.Fprintf(g.logger, "rulegen: fallback attempt %d failed (AI error): %v\n", attempt, callErr)
			continue
		}

		rules, parseErr := parseRulesResponse(response)
		if parseErr != nil {
			entry := transcriptEntry{
				Attempt: attemptCount,
				Rule:    truncate(response, 512),
				Outcome: fmt.Sprintf("error: %v", parseErr),
			}
			transcript = append(transcript, entry)
			_, _ = fmt.Fprintf(g.logger, "rulegen: fallback attempt %d failed (parse error): %v\n", attempt, parseErr)
			continue
		}

		if validateErr := validateRulePatterns(rules); validateErr != nil {
			ruleJSON := marshalRules(rules)
			entry := transcriptEntry{
				Attempt: attemptCount,
				Rule:    truncate(ruleJSON, 512),
				Outcome: fmt.Sprintf("error: %v", validateErr),
			}
			transcript = append(transcript, entry)
			_, _ = fmt.Fprintf(g.logger, "rulegen: fallback attempt %d failed (validate error): %v\n", attempt, validateErr)
			continue
		}

		value, execErr := g.executor.Execute(rules, body, src.BaseCurrency, src.QuoteCurrency)
		ruleJSON := marshalRules(rules)
		if execErr != nil {
			entry := transcriptEntry{
				Attempt: attemptCount,
				Rule:    truncate(ruleJSON, 512),
				Outcome: fmt.Sprintf("error: %v", execErr),
			}
			transcript = append(transcript, entry)
			_, _ = fmt.Fprintf(g.logger, "rulegen: fallback attempt %d failed (exec error): %v\n", attempt, execErr)
			continue
		}

		return g.persist(ctx, src, rules, value, attemptCount, true, g.fallback.Name(), g.fallback.Model())
	}

	return nil, fmt.Errorf("rulegen: all attempts exhausted for source %q (primary=%d, fallback=%d): %w",
		sourceName, g.maxPrimary, g.maxFallback, ErrAttemptsExhausted)
}

func (g *Generator) persist(
	ctx context.Context,
	src *domain.RateSource,
	rules []domain.RateSourceRule,
	value float64,
	attemptsUsed int,
	escalated bool,
	providerName string,
	modelName string,
) (*Result, error) {
	metadata := domain.RateSourceRuleMetadata{
		Provider:     providerName,
		Model:        modelName,
		AttemptsUsed: attemptsUsed,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	src.Rules = rules
	src.RuleMetadata = metadata

	if err := g.sourceRepo.RetainRateSource(ctx, src); err != nil {
		return nil, fmt.Errorf("rulegen: persist rules for source %q: %w", src.Name, err)
	}

	_, _ = fmt.Fprintf(g.logger, "rulegen: success source=%s value=%g attempts=%d escalated=%t provider=%s\n",
		src.Name, value, attemptsUsed, escalated, providerName)

	return &Result{
		Source:       src,
		Rules:        rules,
		Metadata:     metadata,
		Value:        value,
		AttemptsUsed: attemptsUsed,
		Escalated:    escalated,
	}, nil
}

// buildUserMessage constructs the user prompt, including the transcript of
// previous failed attempts when retrying.
func (g *Generator) buildUserMessage(src *domain.RateSource, body []byte, originalSize int, transcript []transcriptEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SOURCE: %s\n", src.Name)
	fmt.Fprintf(&b, "PAIR:   %s/%s  (%s)\n", src.BaseCurrency, src.QuoteCurrency, src.Kind)
	fmt.Fprintf(&b, "URL:    %s\n", src.URL)
	hint := strings.TrimSpace(src.Options.Reserve)
	if hint == "" {
		hint = "none"
	}
	fmt.Fprintf(&b, "HINT:   %s\n\n", hint)
	fmt.Fprintf(&b, "BODY (sectioned to %d KB around anchor, scripts/styles stripped, original was %d bytes):\n", locateWindowBytes/1024, originalSize)
	b.WriteString("----- BEGIN BODY -----\n")
	b.Write(body)
	b.WriteString("\n----- END BODY -----\n")
	if len(transcript) > 0 {
		b.WriteString("\nPREVIOUS ATTEMPTS:\n")
		for _, entry := range transcript {
			fmt.Fprintf(&b, "Attempt %d: rule=%s; outcome=%s\n",
				entry.Attempt,
				truncate(entry.Rule, 512),
				truncate(entry.Outcome, 512),
			)
		}
	}
	return b.String()
}

// parseRulesResponse unmarshals the LLM response into a slice of domain rules.
// It tolerates leading/trailing whitespace and markdown code fences that some
// providers (e.g. Groq) insert around the JSON.
func parseRulesResponse(response string) ([]domain.RateSourceRule, error) {
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var wrapper struct {
		Rules []struct {
			Method  string `json:"method"`
			Pattern string `json:"pattern"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(response), &wrapper); err != nil {
		return nil, fmt.Errorf("parse JSON response: %w", err)
	}

	if len(wrapper.Rules) == 0 {
		return nil, errors.New("response contains no rules")
	}

	rules := make([]domain.RateSourceRule, 0, len(wrapper.Rules))
	for i, r := range wrapper.Rules {
		var method domain.Method
		switch r.Method {
		case "regex":
			method = domain.MethodRegex
		case "json":
			method = domain.MethodJSONPath
		default:
			return nil, fmt.Errorf("rule %d: unsupported method %q", i, r.Method)
		}
		if r.Pattern == "" {
			return nil, fmt.Errorf("rule %d: pattern must not be empty", i)
		}
		rules = append(rules, domain.RateSourceRule{
			Method:  method,
			Pattern: r.Pattern,
		})
	}

	return rules, nil
}

// validateRulePatterns runs regexp.Compile on every MethodRegex rule. It
// returns a descriptive error on the first compile failure so the audit
// loop can feed an RE2-specific hint back to the LLM without exercising
// the executor.
func validateRulePatterns(rules []domain.RateSourceRule) error {
	for i, r := range rules {
		if r.Method != domain.MethodRegex {
			continue
		}
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf(
				"rule %d: regex did not compile: %v; revise the pattern to comply with RE2 syntax "+
					"(no lookarounds, no backreferences, no \\u escapes, no \\/, no possessive quantifiers)",
				i, err,
			)
		}
	}
	return nil
}

// marshalRules returns a compact JSON representation of the rules for logging.
func marshalRules(rules []domain.RateSourceRule) string {
	b, err := json.Marshal(rules)
	if err != nil {
		return "(marshal error)"
	}
	return string(b)
}

// truncate returns s truncated to maxLen runes. Used to cap transcript entries
// before appending them to the next prompt.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
