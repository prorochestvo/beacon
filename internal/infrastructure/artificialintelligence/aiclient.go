// Package artificialintelligence provides AI provider clients used by the rule generator.
// NewClient selects the appropriate driver (OpenAI, Groq, or OpenRouter) from a DSN;
// NewStubClient returns a deterministic fallback for use when no live key is configured.
package artificialintelligence

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/monitor/internal"
)

// AIClient is the interface that all AI provider drivers implement.
// Complete sends systemPrompt and userPrompt to the provider and returns the
// raw text response. CheckUP performs a lightweight liveness probe.
//
// Name returns a human-readable composite identifier such as "Groq[openai/gpt-oss-20b]"
// suitable for log messages and transcripts. Model returns the bare model id
// (e.g. "openai/gpt-oss-20b") for structured storage in rule metadata.
type AIClient interface {
	Name() string
	Model() string
	CheckUP(ctx context.Context) error
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// NewClient parses the driver from dns and dispatches to the matching
// provider constructor. proxyURL is an optional HTTP proxy URL
// (e.g. "http://127.0.0.1:7788"); pass "" for no proxy. On an unknown driver
// it logs to logger and returns the deterministic stub so the service can
// still start without a live key.
func NewClient(dns dsninjector.DataSource, logger io.Writer, proxyURL string) (AIClient, error) {
	switch dns.Driver() {
	case clientOpenAI:
		r, err := newOpenAIClient(dns, logger, proxyURL)
		if err != nil {
			return nil, err
		}
		return r, nil
	case clientGroq:
		r, err := newGroqClient(dns, logger, proxyURL)
		if err != nil {
			return nil, err
		}
		return r, nil
	case clientOpenRouter:
		r, err := newOpenRouterClient(dns, logger, proxyURL)
		if err != nil {
			return nil, err
		}
		return r, nil
	}

	if _, err := fmt.Fprintf(logger, "unsupported driver %q, using default stub client\n", dns.Driver()); err != nil {
		// Best-effort logger write; continue to stub regardless.
		_ = err
	}

	r, err := newStubAIClient(stubAIDefaultResponse)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}
	return r, nil
}

// NewStubClient returns a stub AIClient pre-loaded with the default canned
// response. Use this when no fallback DSN is configured so the service starts
// without a real AI key.
func NewStubClient() (AIClient, error) {
	return newStubAIClient(stubAIDefaultResponse)
}

const (
	clientOpenAI     = "openai"
	clientGroq       = "groq"
	clientOpenRouter = "openrouterai"
)

// parseDSNTimeout reads the "timeout" DSN option and returns a duration clamped
// to [10s, 15m]. When the option is absent or empty, it returns one minute.
func parseDSNTimeout(dns dsninjector.DataSource) (time.Duration, error) {
	value := dns.Option("timeout")
	if value == "" {
		return time.Minute, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, errors.Join(
			fmt.Errorf("unable to parse timeout=%q: %w", value, err),
			internal.NewTraceError(),
		)
	}
	duration = max(duration, 10*time.Second)
	duration = min(duration, 15*time.Minute)
	return duration, nil
}

// buildHTTPClient returns an *http.Client with the given timeout. When proxyURL
// is non-empty it is wired into the transport. When empty, an explicit
// &http.Transport{} with no Proxy field is used — a nil Transport falls back to
// http.DefaultTransport, whose Proxy reads HTTPS_PROXY/HTTP_PROXY from the
// environment and would silently route traffic the caller never configured.
func buildHTTPClient(timeout time.Duration, proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		return &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{}, // explicit empty transport — no Proxy field, no env auto-pickup
		}, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, errors.New("parse proxy URL: invalid format (value redacted from log; check the configured proxy URL)")
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: http.ProxyURL(parsed)},
	}, nil
}

// parseDSNKey reads the DSN password, decodes it as URL-safe base64, and
// returns the plaintext API key. Returns an error when the password is missing,
// not valid URL-safe base64, or decodes to an empty string.
func parseDSNKey(dns dsninjector.DataSource) (string, error) {
	encoded := dns.Password()
	if encoded == "" {
		return "", errors.Join(
			fmt.Errorf("missing API key in DSN"),
			internal.NewTraceError(),
		)
	}

	decoded, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.Join(
			fmt.Errorf("unable to base64-decode API key: %w", err),
			internal.NewTraceError(),
		)
	}

	if len(decoded) == 0 {
		return "", errors.Join(
			fmt.Errorf("missing API key in DSN"),
			internal.NewTraceError(),
		)
	}

	return string(decoded), nil
}
