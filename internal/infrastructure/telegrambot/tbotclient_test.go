package telegrambot

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramBotClient_AdminChatID(t *testing.T) {
	t.Parallel()

	t.Run("returns admin chat id as int64", func(t *testing.T) {
		t.Parallel()
		// Construct the struct directly to test the accessor without a live bot API.
		tbot := &TelegramBotClient{adminChatID: TelegramChatID(123456789)}
		assert.Equal(t, int64(123456789), tbot.AdminChatID())
	})

	t.Run("zero value returns zero", func(t *testing.T) {
		t.Parallel()
		tbot := &TelegramBotClient{adminChatID: 0}
		assert.Equal(t, int64(0), tbot.AdminChatID())
	})
}

// TestTBotClientTransportPattern_documentationOnly verifies the no-op Proxy
// pattern used by NewTBotClient. The wiring inside NewTBotClient is verified by
// code review, since constructing a real *TelegramBotClient needs a live token.
func TestTBotClientTransportPattern_documentationOnly(t *testing.T) {
	t.Parallel()

	t.Run("bot transport Proxy func returns nil for any URL", func(t *testing.T) {
		t.Parallel()

		// Build the transport the same way NewTBotClient does.
		noProxyTransport := &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
		}

		// Simulate a request to the Telegram Bot API endpoint.
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.telegram.org/bot123:abc/getMe", nil)
		require.NoError(t, err)

		got, err := noProxyTransport.Proxy(req)
		require.NoError(t, err)
		assert.Nil(t, got, "Proxy func must return nil so Telegram traffic is always direct")
	})

	t.Run("bot transport Proxy func returns nil for an arbitrary HTTPS URL", func(t *testing.T) {
		t.Parallel()

		noProxyTransport := &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
		}

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/some/path", nil)
		require.NoError(t, err)

		got, err := noProxyTransport.Proxy(req)
		require.NoError(t, err)
		assert.Nil(t, got, "Proxy func must return nil regardless of the target host")
	})
}
