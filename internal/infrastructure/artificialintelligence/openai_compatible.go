package artificialintelligence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/seilbekskindirov/monitor/internal"
)

// chatPingPrompt is the text sent in CheckUP probes. The model is instructed
// to reply with a single word so the token cost per probe is negligible.
const (
	chatPingPrompt        = "Reply with exactly one word and nothing else: pong"
	chatPingExpectedToken = "pong"
	chatPingMaxTokens     = 16
)

const chatPathCompletions = "/chat/completions"

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
}

type openAICompatibleClient struct {
	baseURL      string
	model        string
	apiKey       string
	httpClient   *http.Client
	logger       *log.Logger
	providerName string
}

// complete sends a chat completion request and returns the first choice content.
// When attachSchema is true the request includes a json_schema response_format
// referencing RateExtractionRuleSchema(). When systemPrompt is non-empty it is
// prepended as a system-role message.
func (c *openAICompatibleClient) complete(ctx context.Context, systemPrompt, userMessage string, attachSchema bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.httpClient.Timeout)
	defer cancel()

	urlPath, err := url.JoinPath(c.baseURL, chatPathCompletions)
	if err != nil {
		c.logger.Printf("op=complete stage=join_url err=%v", err)
		return "", errors.Join(err, internal.NewTraceError())
	}

	messages := make([]chatMessage, 0, 2)
	if systemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: userMessage})

	reqBody := chatRequest{
		Model:    c.model,
		Messages: messages,
	}
	if attachSchema {
		reqBody.ResponseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   RateExtractionRuleSchemaName,
				"strict": true,
				"schema": RateExtractionRuleSchema(),
			},
		}
	}

	return c.doRequest(ctx, urlPath, reqBody, "complete")
}

// ping sends a minimal chat completion request to verify liveness and auth.
// The model, prompt, expected substring, and max-tokens are all parametric so
// each provider wrapper can supply its own cheap probe configuration.
func (c *openAICompatibleClient) ping(ctx context.Context, model, prompt, expected string, maxTokens int) error {
	ctx, cancel := context.WithTimeout(ctx, c.httpClient.Timeout)
	defer cancel()

	urlPath, err := url.JoinPath(c.baseURL, chatPathCompletions)
	if err != nil {
		c.logger.Printf("op=checkup stage=join_url err=%v", err)
		return errors.Join(err, internal.NewTraceError())
	}

	reqBody := chatRequest{
		Model:     model,
		Messages:  []chatMessage{{Role: "user", Content: prompt}},
		MaxTokens: maxTokens,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		c.logger.Printf("op=checkup stage=marshal err=%v", err)
		return errors.Join(fmt.Errorf("%s: checkup: marshal: %w", c.providerName, err), internal.NewTraceError())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlPath, bytes.NewReader(body))
	if err != nil {
		c.logger.Printf("op=checkup stage=new_request err=%v", err)
		return errors.Join(fmt.Errorf("%s: checkup: new request: %w", c.providerName, err), internal.NewTraceError())
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Printf("[ERR] %s %s (model=%s err=%v)", req.Method, req.URL.Path, model, err)
		return errors.Join(fmt.Errorf("%s: checkup: do: %w", c.providerName, err), internal.NewTraceError())
	}
	defer resp.Body.Close()

	c.logger.Printf("[%.3d] %s %s (model=%s)", resp.StatusCode, req.Method, req.URL.Path, model)

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		c.logger.Printf("op=checkup stage=read_body http_status=%d err=%v", resp.StatusCode, err)
		return errors.Join(
			fmt.Errorf("%s: checkup: read body (status %d): %w", c.providerName, resp.StatusCode, err),
			internal.NewTraceError(),
		)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.logger.Printf("op=checkup stage=non_2xx url=%s http_status=%d body=%s", urlPath, resp.StatusCode, string(rawBody))
		return errors.Join(
			fmt.Errorf("%s: checkup: unexpected status %d at %s: %s", c.providerName, resp.StatusCode, urlPath, string(rawBody)),
			internal.NewTraceError(),
		)
	}

	trimmed := bytes.TrimSpace(rawBody)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		c.logger.Printf("op=checkup stage=non_json_body url=%s http_status=%d body=%s", urlPath, resp.StatusCode, string(rawBody))
		return errors.Join(
			fmt.Errorf("%s: checkup: non-JSON body at %s (status %d): %s", c.providerName, urlPath, resp.StatusCode, string(rawBody)),
			internal.NewTraceError(),
		)
	}

	var result chatResponse
	if err = json.Unmarshal(rawBody, &result); err != nil {
		preview := string(rawBody)
		if len(preview) > 128 {
			preview = preview[:128] + "..."
		}
		c.logger.Printf("op=checkup stage=decode http_status=%d err=%v body=%s", resp.StatusCode, err, preview)
		return errors.Join(
			fmt.Errorf("%s: checkup: decode (status %d, body=%s): %w", c.providerName, resp.StatusCode, preview, err),
			internal.NewTraceError(),
		)
	}

	if len(result.Choices) == 0 {
		c.logger.Printf("op=checkup stage=empty_choices")
		return errors.Join(
			fmt.Errorf("%s: checkup: empty choices in response", c.providerName),
			internal.NewTraceError(),
		)
	}

	content := result.Choices[0].Message.Content
	if !strings.Contains(strings.ToLower(content), expected) {
		c.logger.Printf("op=checkup stage=missing_expected_token expected=%s got=%s", expected, content)
		return errors.Join(
			fmt.Errorf("%s: checkup: response does not contain %q: %s", c.providerName, expected, content),
			internal.NewTraceError(),
		)
	}

	return nil
}

// doRequest marshals reqBody, POSTs to urlPath, reads the response, and
// returns the first choice message content. The op label appears in log lines.
func (c *openAICompatibleClient) doRequest(ctx context.Context, urlPath string, reqBody chatRequest, op string) (string, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		c.logger.Printf("op=%s stage=marshal err=%v", op, err)
		return "", errors.Join(fmt.Errorf("%s: %s: marshal: %w", c.providerName, op, err), internal.NewTraceError())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlPath, bytes.NewReader(body))
	if err != nil {
		c.logger.Printf("op=%s stage=new_request err=%v", op, err)
		return "", errors.Join(fmt.Errorf("%s: %s: new request: %w", c.providerName, op, err), internal.NewTraceError())
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Printf("[ERR] %s %s (model=%s err=%v)", req.Method, req.URL.Path, reqBody.Model, err)
		return "", errors.Join(fmt.Errorf("%s: %s: do: %w", c.providerName, op, err), internal.NewTraceError())
	}
	defer resp.Body.Close()

	c.logger.Printf("[%.3d] %s %s (model=%s)", resp.StatusCode, req.Method, req.URL.Path, reqBody.Model)

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		c.logger.Printf("op=%s stage=read_body http_status=%d err=%v", op, resp.StatusCode, err)
		return "", errors.Join(
			fmt.Errorf("%s: %s: read body (status %d): %w", c.providerName, op, resp.StatusCode, err),
			internal.NewTraceError(),
		)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.logger.Printf("op=%s stage=non_2xx url=%s http_status=%d body=%s", op, urlPath, resp.StatusCode, string(rawBody))
		return "", errors.Join(
			fmt.Errorf("%s: %s: unexpected status %d at %s: %s", c.providerName, op, resp.StatusCode, urlPath, string(rawBody)),
			internal.NewTraceError(),
		)
	}

	trimmed := bytes.TrimSpace(rawBody)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		c.logger.Printf("op=%s stage=non_json_body url=%s http_status=%d body=%s", op, urlPath, resp.StatusCode, string(rawBody))
		return "", errors.Join(
			fmt.Errorf("%s: %s: non-JSON body at %s (status %d): %s", c.providerName, op, urlPath, resp.StatusCode, string(rawBody)),
			internal.NewTraceError(),
		)
	}

	var result chatResponse
	if err = json.Unmarshal(rawBody, &result); err != nil {
		preview := string(rawBody)
		if len(preview) > 128 {
			preview = preview[:128] + "..."
		}
		c.logger.Printf("op=%s stage=decode http_status=%d err=%v body=%s", op, resp.StatusCode, err, preview)
		return "", errors.Join(
			fmt.Errorf("%s: %s: decode (status %d, body=%s): %w", c.providerName, op, resp.StatusCode, preview, err),
			internal.NewTraceError(),
		)
	}

	if len(result.Choices) == 0 {
		c.logger.Printf("op=%s stage=empty_choices", op)
		return "", errors.Join(
			fmt.Errorf("%s: %s: empty choices in response", c.providerName, op),
			internal.NewTraceError(),
		)
	}

	return result.Choices[0].Message.Content, nil
}
