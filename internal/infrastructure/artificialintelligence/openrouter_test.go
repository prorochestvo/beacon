package artificialintelligence

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ AIClient = &openRouterClient{}

func newTestOpenRouterClient(t *testing.T, baseURL string) *openRouterClient {
	t.Helper()
	return &openRouterClient{
		inner: openAICompatibleClient{
			baseURL:      baseURL,
			model:        "openai/gpt-4o",
			apiKey:       "test-key",
			httpClient:   &http.Client{Timeout: 2 * time.Second},
			logger:       log.New(io.Discard, "", 0),
			providerName: "openrouter",
		},
	}
}

func validChatResponseJSON(t *testing.T, content string) []byte {
	t.Helper()
	resp := chatResponse{
		Choices: []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		}{
			{Message: chatMessage{Role: "assistant", Content: content}, FinishReason: "stop"},
		},
	}
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	return data
}

func TestOpenRouterClient_Complete(t *testing.T) {
	t.Parallel()

	t.Run("success with system message", func(t *testing.T) {
		t.Parallel()

		var receivedBody []byte
		var receivedPath, receivedMethod, receivedAuth, receivedContentType string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedPath = r.URL.Path
			receivedMethod = r.Method
			receivedAuth = r.Header.Get("Authorization")
			receivedContentType = r.Header.Get("Content-Type")
			var err error
			receivedBody, err = io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "rate result"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		result, err := client.Complete(t.Context(), "sys prompt", "user message")
		require.NoError(t, err)
		assert.Equal(t, "rate result", result)

		assert.Equal(t, http.MethodPost, receivedMethod)
		assert.True(t, strings.HasSuffix(receivedPath, "/chat/completions"),
			"expected path to end with /chat/completions, got %q", receivedPath)
		assert.Equal(t, "Bearer test-key", receivedAuth)
		assert.Equal(t, "application/json", receivedContentType)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(receivedBody, &decoded))

		messagesRaw, ok := decoded["messages"].([]any)
		require.True(t, ok, "messages must be a JSON array")
		require.Len(t, messagesRaw, 2)

		systemMsg, ok := messagesRaw[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "system", systemMsg["role"])
		assert.Equal(t, "sys prompt", systemMsg["content"])

		userMsg, ok := messagesRaw[1].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "user", userMsg["role"])
		assert.Equal(t, "user message", userMsg["content"])

		responseFormat, ok := decoded["response_format"].(map[string]any)
		require.True(t, ok, "response_format must be present")
		jsonSchema, ok := responseFormat["json_schema"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, RateExtractionRuleSchemaName, jsonSchema["name"])
	})

	t.Run("success without system message sends one-element messages array", func(t *testing.T) {
		t.Parallel()

		var receivedBody []byte

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var err error
			receivedBody, err = io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "rate result"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		result, err := client.Complete(t.Context(), "", "user message")
		require.NoError(t, err)
		assert.Equal(t, "rate result", result)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(receivedBody, &decoded))

		messagesRaw, ok := decoded["messages"].([]any)
		require.True(t, ok, "messages must be a JSON array")
		require.Len(t, messagesRaw, 1)

		userMsg, ok := messagesRaw[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "user", userMsg["role"])
	})

	t.Run("returns error on http 401", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, werr := w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		_, err := client.Complete(t.Context(), "sys", "user")
		require.Error(t, err)
	})

	t.Run("returns error on http 500", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, werr := w.Write([]byte(`{"error":{"message":"internal server error"}}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		_, err := client.Complete(t.Context(), "sys", "user")
		require.Error(t, err)
	})

	t.Run("returns error when choices array is empty", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write([]byte(`{"choices":[]}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		_, err := client.Complete(t.Context(), "sys", "user")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty choices")
	})

	t.Run("returns error when response body is malformed JSON", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write([]byte(`{"invalid`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		_, err := client.Complete(t.Context(), "sys", "user")
		require.Error(t, err)
	})

	t.Run("returns error with full body when response is not JSON", func(t *testing.T) {
		t.Parallel()

		htmlBody := `<!DOCTYPE html><html><body>Cloudflare error 1020</body></html>`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "text/html")
			_, werr := w.Write([]byte(htmlBody))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		_, err := client.Complete(t.Context(), "sys", "user")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-JSON body")
		assert.Contains(t, err.Error(), htmlBody, "full body must appear in the error")
	})

	t.Run("returns error when context is cancelled before request", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "should not reach"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := client.Complete(ctx, "sys", "user")
		require.Error(t, err)
	})
}

func TestOpenRouterClient_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("success when response contains the expected token", func(t *testing.T) {
		t.Parallel()

		var receivedBody []byte
		var receivedPath, receivedMethod, receivedAuth, receivedContentType string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedPath = r.URL.Path
			receivedMethod = r.Method
			receivedAuth = r.Header.Get("Authorization")
			receivedContentType = r.Header.Get("Content-Type")
			var err error
			receivedBody, err = io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "pong"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		err := client.CheckUP(t.Context())
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, receivedMethod)
		assert.True(t, strings.HasSuffix(receivedPath, "/chat/completions"),
			"expected path to end with /chat/completions, got %q", receivedPath)
		assert.Equal(t, "Bearer test-key", receivedAuth)
		assert.Equal(t, "application/json", receivedContentType)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(receivedBody, &decoded))
		assert.Equal(t, openRouterCheckUPModel, decoded["model"],
			"checkup must use the cheap probe model regardless of client.model")
		assert.EqualValues(t, chatPingMaxTokens, decoded["max_tokens"])
	})

	t.Run("success is case-insensitive for the expected token", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "PONG!"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		err := client.CheckUP(t.Context())
		require.NoError(t, err)
	})

	t.Run("returns error when response content does not contain the expected token", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "I cannot do that"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		err := client.CheckUP(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), chatPingExpectedToken)
	})

	t.Run("returns error when choices array is empty", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write([]byte(`{"choices":[]}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		err := client.CheckUP(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty choices")
	})

	t.Run("returns error with full body when response is not JSON", func(t *testing.T) {
		t.Parallel()

		htmlBody := `<!DOCTYPE html><html><body>Cloudflare error 1020</body></html>`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, werr := w.Write([]byte(htmlBody))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		err := client.CheckUP(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-JSON body")
		assert.Contains(t, err.Error(), htmlBody, "full body must appear in the error")
	})

	t.Run("returns error on http 401", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, werr := w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		err := client.CheckUP(t.Context())
		require.Error(t, err)
	})

	t.Run("returns error when context is cancelled before request", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "pong"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenRouterClient(t, server.URL)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := client.CheckUP(ctx)
		require.Error(t, err)
	})
}

func TestOpenRouterClient_Name(t *testing.T) {
	t.Parallel()

	t.Run("includes the model in the name", func(t *testing.T) {
		t.Parallel()
		c := &openRouterClient{
			inner: openAICompatibleClient{model: "anthropic/claude-3.5-sonnet"},
		}
		assert.Equal(t, "OpenRouter[anthropic/claude-3.5-sonnet]", c.Name())
	})
}

func TestOpenRouterClient_Model(t *testing.T) {
	t.Parallel()

	t.Run("returns bare model id without provider prefix", func(t *testing.T) {
		t.Parallel()
		c := &openRouterClient{
			inner: openAICompatibleClient{model: "anthropic/claude-3.5-sonnet"},
		}
		assert.Equal(t, "anthropic/claude-3.5-sonnet", c.Model())
	})
}

func TestOpenRouterClient_BaseURL(t *testing.T) {
	t.Parallel()

	t.Run("constructs correct base URL from host and database path", func(t *testing.T) {
		t.Parallel()
		var capturedURL string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedURL = r.URL.RequestURI()
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "pong"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		// Simulate DSN where Database() returns "api/v1"
		client := &openRouterClient{
			inner: openAICompatibleClient{
				baseURL:      server.URL + "/api/v1",
				model:        "openai/gpt-4o",
				apiKey:       "test-key",
				httpClient:   &http.Client{Timeout: 2 * time.Second},
				logger:       log.New(&bytes.Buffer{}, "", 0),
				providerName: "openrouter",
			},
		}
		err := client.CheckUP(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "/api/v1/chat/completions", capturedURL,
			"base URL with /api/v1 must produce /api/v1/chat/completions")
	})
}
