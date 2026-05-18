// Groq client.
//
// The Groq REST API is OpenAI-compatible at the chat-completions path, so this
// driver is a thin wrapper over the shared openAICompatibleClient.
//
// DSN format:
//
//	groq://_:<base64url(KEY)>@api.groq.com/openai/v1?model=<model>&timeout=<dur>
//
// Default model: openai/gpt-oss-20b
//
// Rationale: as of 2026-05-14, Groq's structured-output strict mode
// (response_format: { type: "json_schema", strict: true }) is only supported
// by the openai/gpt-oss-20b and openai/gpt-oss-120b families. The lighter
// llama-3.1-8b-instant model does NOT support strict json_schema and returns
// HTTP 400. openai/gpt-oss-20b is the cheapest model in the strict-schema
// tier ($0.075/$0.30 per 1M tokens) and has been chosen as the default.
// Before changing this default, verify the replacement model's strict-schema
// support at https://console.groq.com/docs/structured-outputs.
package artificialintelligence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/monitor/internal"
)

const groqDefaultModel = "openai/gpt-oss-20b"

// newGroqClient parses the DSN and returns a ready-to-use groqClient.
//
// DSN: groq://_:<base64url(KEY)>@api.groq.com/openai/v1?model=<model>&timeout=<dur>
func newGroqClient(dns dsninjector.DataSource, logger io.Writer) (*groqClient, error) {
	apiKey, err := parseDSNKey(dns)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}

	baseURL, err := url.JoinPath(fmt.Sprintf("https://%s", dns.Addr()), dns.Database())
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}

	model := groqDefaultModel
	if v := dns.Option("model"); v != "" {
		model = v
	}

	timeout, err := parseDSNTimeout(dns)
	if err != nil {
		return nil, errors.Join(err, internal.NewTraceError())
	}

	return &groqClient{
		inner: openAICompatibleClient{
			baseURL:      baseURL,
			model:        model,
			apiKey:       apiKey,
			httpClient:   &http.Client{Timeout: timeout},
			logger:       log.New(logger, "groq ", log.LstdFlags),
			providerName: "groq",
		},
	}, nil
}

type groqClient struct {
	inner openAICompatibleClient
}

func (c *groqClient) Name() string {
	return fmt.Sprintf("Groq[%s]", c.inner.model)
}

func (c *groqClient) Model() string {
	return c.inner.model
}

// CheckUP verifies the Groq API is reachable and credentials are valid by
// sending a minimal chat completion request.
func (c *groqClient) CheckUP(ctx context.Context) error {
	return c.inner.ping(ctx, c.inner.model, chatPingPrompt, chatPingExpectedToken, chatPingMaxTokens)
}

// Complete sends a chat completion request to Groq and returns the content of
// the first choice message. A json_schema response_format is always attached.
func (c *groqClient) Complete(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	return c.inner.complete(ctx, systemPrompt, userMessage, true)
}
