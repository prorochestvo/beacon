// OpenRouter client.
//
// DSN format:
//
//	openrouterai://_:<base64url(KEY)>@openrouter.ai/api/v1?model=openai/gpt-4o&timeout=30s
//
// The DSN host and database fields together form the API base URL
// (https://<host>/<database>); the chat-completions sub-path is appended at
// request time.
//
// Encoding: the API key (DSN password) is URL-safe base64-encoded in the DSN.
// This protects it against accidental escaping by shells, env-file loaders, and
// DSN query-string parsers. The decoder lives in aiclient.go — see parseDSNKey.
//
// Model validation is intentionally absent: OpenRouter supports many providers
// (e.g. anthropic/claude-..., google/gemini-...) and the caller must supply a
// model string the selected plan supports. Not all OpenRouter models support
// response_format:json_schema — an unsupported one returns HTTP 400 at runtime.
package artificialintelligence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/monitor/internal"
)

// openRouterCheckUPModel is the cheapest paid variant on OpenRouter, used for
// liveness probes. The paid 1B model has its own quota and avoids the heavy
// rate-limiting on :free tier models.
const openRouterCheckUPModel = "meta-llama/llama-3.2-1b-instruct"

// newOpenRouterClient parses the DSN and returns a ready-to-use openRouterClient.
//
// DSN: openrouterai://_:<base64url(KEY)>@<host>/<base-path>?model=<model>&timeout=<duration>
//
// proxyURL is an optional HTTP proxy URL (e.g. "http://127.0.0.1:7788"); pass "" for none.
func newOpenRouterClient(dns dsninjector.DataSource, logger io.Writer, proxyURL string) (*openRouterClient, error) {
	apiKey, err := parseDSNKey(dns)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}

	baseURL, err := url.JoinPath(fmt.Sprintf("https://%s", dns.Addr()), dns.Database())
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}

	model := "openai/gpt-4o"
	if v := dns.Option("model"); v != "" {
		model = v
	}

	timeout, err := parseDSNTimeout(dns)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}

	httpClient, err := buildHTTPClient(timeout, proxyURL)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}

	return &openRouterClient{
		inner: openAICompatibleClient{
			baseURL:      baseURL,
			model:        model,
			apiKey:       apiKey,
			httpClient:   httpClient,
			logger:       log.New(logger, "openrouter ", log.LstdFlags),
			providerName: "openrouter",
		},
	}, nil
}

type openRouterClient struct {
	inner openAICompatibleClient
}

func (c *openRouterClient) Name() string {
	return fmt.Sprintf("OpenRouter[%s]", c.inner.model)
}

func (c *openRouterClient) Model() string {
	return c.inner.model
}

// CheckUP verifies the OpenRouter API is reachable and credentials are valid by
// sending a minimal chat completion request using the cheap probe model.
func (c *openRouterClient) CheckUP(ctx context.Context) error {
	return c.inner.ping(ctx, openRouterCheckUPModel, chatPingPrompt, chatPingExpectedToken, chatPingMaxTokens)
}

// Complete sends a chat completion request to OpenRouter and returns the content
// of the first choice message. A json_schema response_format is always attached.
func (c *openRouterClient) Complete(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	return c.inner.complete(ctx, systemPrompt, userMessage, true)
}
