// OpenAI client (thin wrapper over the OpenAI Responses API).
//
// DSN format:
//
//	openai://_:<base64url(KEY)>@api.openai.com/v1?model=<model>&timeout=<dur>
//
// The client is intentionally thin — no retry logic, no stored-prompt
// indirection. The rule-generator passes an inline system prompt on every call.
//
// Encoding: the API key (DSN password) is URL-safe base64-encoded in the DSN.
// This protects it against accidental escaping by shells, env-file loaders, and
// DSN query-string parsers. The decoder lives in aiclient.go — see parseDSNKey.
//
// Structured Output: every Complete() call attaches a json_schema response
// format describing the rate_extraction_rule shape, so the model must return
// that exact JSON. Only models in OpenAIModels (gpt-4o family and newer) are
// accepted — others do not support strict structured output and produce HTTP 400.
package artificialintelligence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/prorochestvo/dsninjector"
	"github.com/prorochestvo/loginjector"
)

// OpenAIModels is the allowlist of models that support Structured Output
// (strict json_schema mode). Any other model string is rejected at construction
// time so we fail fast rather than at first call.
var OpenAIModels = []string{
	openaisdk.ChatModelGPT5_4,
	openaisdk.ChatModelGPT5_4Mini,
	openaisdk.ChatModelGPT5_4Nano,
	openaisdk.ChatModelGPT5_2,
	openaisdk.ChatModelGPT4o,
}

// newOpenAIClient parses the DSN and returns a ready-to-use openAIClient.
//
// DSN: openai://_:<base64url(KEY)>@<host>/<path>?model=<model>&timeout=<duration>
//
// proxyURL is an optional HTTP proxy URL (e.g. "http://127.0.0.1:7788"); pass ""
// for none. The HTTP client is built via buildHTTPClient and injected into the
// SDK via option.WithHTTPClient so all SDK requests honour the explicit proxy
// setting without falling back to HTTPS_PROXY env vars.
func newOpenAIClient(dns dsninjector.DataSource, logger io.Writer, proxyURL string) (AIClient, error) {
	apiKey, err := parseDSNKey(dns)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	model := shared.ChatModel(openaisdk.ChatModelGPT4o)
	if v := dns.Option("model"); v != "" {
		found := false
		for _, m := range OpenAIModels {
			if m == v {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.Join(
				fmt.Errorf("unsupported model %q", v),
				loginjector.NewTraceError(),
			)
		}
		model = shared.ChatModel(v)
	}

	timeout, err := parseDSNTimeout(dns)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	httpClient, err := buildHTTPClient(timeout, proxyURL)
	if err != nil {
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	api := openaisdk.NewClient(
		option.WithEnvironmentProduction(),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient),
	)

	return &openAIClient{
		model:      model,
		api:        api,
		logger:     log.New(logger, "openai ", log.LstdFlags),
		timeout:    timeout,
		httpClient: httpClient,
	}, nil
}

type openAIClient struct {
	model      shared.ChatModel
	api        openaisdk.Client
	logger     *log.Logger
	timeout    time.Duration
	httpClient *http.Client // retained for test-transport introspection
}

func (c *openAIClient) Name() string {
	return fmt.Sprintf("OpenAI[%s]", c.model)
}

func (c *openAIClient) Model() string {
	return string(c.model)
}

// CheckUP verifies the OpenAI API is reachable and credentials are valid by
// listing available models.
func (c *openAIClient) CheckUP(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	page, err := c.api.Models.List(ctx)
	if err != nil {
		c.logger.Printf("op=checkup stage=models_list err=%v", err)
		return errors.Join(fmt.Errorf("openai: checkup: %w", err), loginjector.NewTraceError())
	}
	if page == nil || len(page.Data) == 0 {
		c.logger.Printf("op=checkup stage=empty_models_list")
		return errors.Join(errors.New("openai: checkup: empty models list"), loginjector.NewTraceError())
	}
	return nil
}

// Complete sends a request to the Responses endpoint and returns the raw output
// text. systemPrompt is forwarded as inline instructions; userMessage is the
// input string. A json_schema response_format is always attached.
func (c *openAIClient) Complete(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	params := responses.ResponseNewParams{
		Model: c.model,
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:   RateExtractionRuleSchemaName,
					Schema: RateExtractionRuleSchema(),
					Strict: openaisdk.Bool(true),
				},
			},
		},
		Instructions: openaisdk.String(systemPrompt),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openaisdk.String(userMessage),
		},
	}

	result, err := c.api.Responses.New(ctx, params)
	if err != nil {
		c.logger.Printf("op=complete stage=responses_new err=%v", err)
		return "", errors.Join(fmt.Errorf("openai: complete: %w", err), loginjector.NewTraceError())
	}

	return result.OutputText(), nil
}
