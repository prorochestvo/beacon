package artificialintelligence

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
	"github.com/prorochestvo/dsninjector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ AIClient = &openAIClient{}

func newTestOpenAIClient(t *testing.T, baseURL string) *openAIClient {
	t.Helper()
	api := openaisdk.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(baseURL),
	)
	return &openAIClient{
		model:   shared.ChatModel(openaisdk.ChatModelGPT4o),
		api:     api,
		logger:  log.New(io.Discard, "", 0),
		timeout: 10 * time.Second,
	}
}

// minimalResponsesJSON returns the smallest valid Responses API body that
// makes OutputText() return content. The SDK walks output[].content[] looking
// for type=="output_text" items.
func minimalResponsesJSON(t *testing.T, content string) []byte {
	t.Helper()
	body := map[string]any{
		"id":     "resp_test",
		"object": "response",
		"status": "completed",
		"model":  openaisdk.ChatModelGPT4o,
		"output": []map[string]any{
			{
				"id":     "msg_test",
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{
					{
						"type": "output_text",
						"text": content,
					},
				},
			},
		},
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	return data
}

func TestOpenAIClient_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when models endpoint responds with non-empty list", func(t *testing.T) {
		t.Parallel()

		var gotPath, gotAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o","object":"model","created":0,"owned_by":"openai"}]}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenAIClient(t, server.URL+"/v1/")
		err := client.CheckUP(t.Context())
		require.NoError(t, err)
		assert.True(t, strings.HasSuffix(gotPath, "/models"),
			"expected request to /models, got %q", gotPath)
		assert.Equal(t, "Bearer test-key", gotAuth)
	})

	t.Run("returns error when API responds with 401", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, werr := w.Write([]byte(`{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenAIClient(t, server.URL+"/v1/")
		err := client.CheckUP(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "openai: checkup")
	})

	t.Run("returns error when models list is empty", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write([]byte(`{"object":"list","data":[]}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenAIClient(t, server.URL+"/v1/")
		err := client.CheckUP(t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty models list")
	})

	t.Run("returns error when parent context is cancelled", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o","object":"model","created":0,"owned_by":"openai"}]}`))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenAIClient(t, server.URL+"/v1/")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := client.CheckUP(ctx)
		require.Error(t, err)
	})
}

func TestOpenAIClient_Model(t *testing.T) {
	t.Parallel()

	t.Run("returns bare model id without provider prefix", func(t *testing.T) {
		t.Parallel()
		c := &openAIClient{model: shared.ChatModel(openaisdk.ChatModelGPT4o)}
		assert.Equal(t, openaisdk.ChatModelGPT4o, c.Model())
	})
}

func TestOpenAIClient_Complete(t *testing.T) {
	t.Parallel()

	t.Run("inline system message", func(t *testing.T) {
		t.Parallel()

		var receivedBody []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var err error
			receivedBody, err = io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(minimalResponsesJSON(t, "extraction result"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenAIClient(t, server.URL+"/v1/")
		result, err := client.Complete(t.Context(), "sys instructions", "user msg")
		require.NoError(t, err)
		assert.Equal(t, "extraction result", result)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(receivedBody, &decoded))

		assert.Equal(t, "sys instructions", decoded["instructions"])
		assert.Equal(t, "user msg", decoded["input"])
		_, hasPrompt := decoded["prompt"]
		assert.False(t, hasPrompt, "prompt field must be absent — no stored-prompt mode")

		textField, ok := decoded["text"].(map[string]any)
		require.True(t, ok, "text field must be present")
		formatField, ok := textField["format"].(map[string]any)
		require.True(t, ok, "text.format must be present")
		assert.Equal(t, RateExtractionRuleSchemaName, formatField["name"])
	})

	t.Run("empty system message still sends instructions field", func(t *testing.T) {
		t.Parallel()

		var receivedBody []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var err error
			receivedBody, err = io.ReadAll(r.Body)
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_, werr := w.Write(minimalResponsesJSON(t, "result"))
			require.NoError(t, werr)
		}))
		t.Cleanup(server.Close)

		client := newTestOpenAIClient(t, server.URL+"/v1/")
		result, err := client.Complete(t.Context(), "", "user msg")
		require.NoError(t, err)
		assert.Equal(t, "result", result)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(receivedBody, &decoded))
		assert.Equal(t, "user msg", decoded["input"])
	})
}

// TestNewOpenAIClient verifies newOpenAIClient threads proxyURL into the
// underlying *http.Transport. The OpenAI SDK builds its own transport; without
// option.WithHTTPClient the proxy is silently ignored.
func TestNewOpenAIClient(t *testing.T) {
	t.Parallel()

	// buildValidDSN returns a minimal openai DSN with a real base64-encoded key.
	buildValidDSN := func(t *testing.T) dsninjector.DataSource {
		t.Helper()
		ds := &dsninjector.DataSourceMapper{}
		ds.SetDriver("openai")
		ds.SetPassword(base64.URLEncoding.EncodeToString([]byte("sk-test-key")))
		return ds
	}

	t.Run("non-empty proxyURL results in non-nil Proxy on the injected transport", func(t *testing.T) {
		t.Parallel()
		client, err := newOpenAIClient(buildValidDSN(t), io.Discard, "http://127.0.0.1:7788")
		require.NoError(t, err)
		oc, ok := client.(*openAIClient)
		require.True(t, ok)
		transport, ok := oc.httpClient.Transport.(*http.Transport)
		require.True(t, ok, "injected transport must be *http.Transport")
		assert.NotNil(t, transport.Proxy, "Proxy func must be set when proxyURL is non-empty")
	})

	t.Run("empty proxyURL results in nil Proxy on the injected transport", func(t *testing.T) {
		t.Parallel()
		client, err := newOpenAIClient(buildValidDSN(t), io.Discard, "")
		require.NoError(t, err)
		oc, ok := client.(*openAIClient)
		require.True(t, ok)
		transport, ok := oc.httpClient.Transport.(*http.Transport)
		require.True(t, ok, "injected transport must be *http.Transport")
		assert.Nil(t, transport.Proxy,
			"Proxy func must be nil when proxyURL is empty — prevents HTTPS_PROXY env auto-pickup")
	})
}
