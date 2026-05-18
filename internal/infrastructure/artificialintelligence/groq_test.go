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

var _ AIClient = &groqClient{}

func newTestGroqClient(t *testing.T, baseURL string) *groqClient {
	t.Helper()
	return &groqClient{
		inner: openAICompatibleClient{
			baseURL:      baseURL,
			model:        groqDefaultModel,
			apiKey:       "test-key",
			httpClient:   &http.Client{Timeout: 2 * time.Second},
			logger:       log.New(io.Discard, "", 0),
			providerName: "groq",
		},
	}
}

func TestGroqClient_Complete(t *testing.T) {
	t.Parallel()

	t.Run("success with system message", func(t *testing.T) {
		t.Parallel()

		var receivedBody []byte
		var receivedPath, receivedMethod, receivedAuth string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedPath = r.URL.Path
			receivedMethod = r.Method
			receivedAuth = r.Header.Get("Authorization")
			var err error
			receivedBody, err = io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "groq rate result"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestGroqClient(t, server.URL)
		result, err := client.Complete(t.Context(), "sys prompt", "user message")
		require.NoError(t, err)
		assert.Equal(t, "groq rate result", result)

		assert.Equal(t, http.MethodPost, receivedMethod)
		assert.True(t, strings.HasSuffix(receivedPath, "/chat/completions"),
			"expected path to end with /chat/completions, got %q", receivedPath)
		assert.Equal(t, "Bearer test-key", receivedAuth)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(receivedBody, &decoded))

		messagesRaw, ok := decoded["messages"].([]any)
		require.True(t, ok, "messages must be a JSON array")
		require.Len(t, messagesRaw, 2)

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
			_, werr := w.Write(validChatResponseJSON(t, "result"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestGroqClient(t, server.URL)
		result, err := client.Complete(t.Context(), "", "user message")
		require.NoError(t, err)
		assert.Equal(t, "result", result)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(receivedBody, &decoded))

		messagesRaw, ok := decoded["messages"].([]any)
		require.True(t, ok)
		require.Len(t, messagesRaw, 1)
	})

	t.Run("returns error on http 401", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			w.WriteHeader(http.StatusUnauthorized)
			_, werr := w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestGroqClient(t, server.URL)
		_, err := client.Complete(t.Context(), "sys", "user")
		require.Error(t, err)
	})

	t.Run("returns error on http 500", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			w.WriteHeader(http.StatusInternalServerError)
			_, werr := w.Write([]byte(`{"error":{"message":"internal server error"}}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestGroqClient(t, server.URL)
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

		client := newTestGroqClient(t, server.URL)
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

		client := newTestGroqClient(t, server.URL)
		_, err := client.Complete(t.Context(), "sys", "user")
		require.Error(t, err)
	})

	t.Run("returns error with full body when response is not JSON", func(t *testing.T) {
		t.Parallel()

		htmlBody := `<!DOCTYPE html><html><body>Cloudflare error</body></html>`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "text/html")
			_, werr := w.Write([]byte(htmlBody))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestGroqClient(t, server.URL)
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

		client := newTestGroqClient(t, server.URL)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := client.Complete(ctx, "sys", "user")
		require.Error(t, err)
	})
}

func TestGroqClient_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("success when response contains the expected token", func(t *testing.T) {
		t.Parallel()

		var receivedBody []byte

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var err error
			receivedBody, err = io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(validChatResponseJSON(t, "pong"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestGroqClient(t, server.URL)
		err := client.CheckUP(t.Context())
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(receivedBody, &decoded))
		assert.Equal(t, groqDefaultModel, decoded["model"],
			"Groq CheckUP must use the production model as the probe (no separate cheap probe model)")
	})

	t.Run("returns error on http 401", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, werr := w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestGroqClient(t, server.URL)
		err := client.CheckUP(t.Context())
		require.Error(t, err)
	})

	t.Run("returns error on http 500", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, werr := w.Write([]byte(`{"error":{"message":"boom"}}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestGroqClient(t, server.URL)
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

		client := newTestGroqClient(t, server.URL)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := client.CheckUP(ctx)
		require.Error(t, err)
	})
}

func TestGroqClient_Name(t *testing.T) {
	t.Parallel()

	t.Run("includes the model in the name", func(t *testing.T) {
		t.Parallel()
		c := &groqClient{
			inner: openAICompatibleClient{model: groqDefaultModel},
		}
		assert.Equal(t, "Groq["+groqDefaultModel+"]", c.Name())
	})
}

func TestGroqClient_Model(t *testing.T) {
	t.Parallel()

	t.Run("returns bare model id without provider prefix", func(t *testing.T) {
		t.Parallel()
		c := &groqClient{
			inner: openAICompatibleClient{model: groqDefaultModel},
		}
		assert.Equal(t, groqDefaultModel, c.Model())
	})
}

func TestGroqClient_BaseURL(t *testing.T) {
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

		// Simulate DSN where path is /openai/v1 (Groq's actual path)
		client := &groqClient{
			inner: openAICompatibleClient{
				baseURL:      server.URL + "/openai/v1",
				model:        groqDefaultModel,
				apiKey:       "test-key",
				httpClient:   &http.Client{Timeout: 2 * time.Second},
				logger:       log.New(&bytes.Buffer{}, "", 0),
				providerName: "groq",
			},
		}
		err := client.CheckUP(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "/openai/v1/chat/completions", capturedURL,
			"Groq base URL /openai/v1 must produce /openai/v1/chat/completions")
	})
}
