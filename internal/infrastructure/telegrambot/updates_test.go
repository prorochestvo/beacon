package telegrambot

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFakeBotAPI points a real *tgbotapi.BotAPI at a local server, so the polling
// loop under test runs against the library rather than around it.
//
// getUpdates answers with one update the first time and nothing afterwards,
// which is the shape of an idle bot: the loop must keep waiting rather than
// treating an empty poll as the end.
func newFakeBotAPI(t *testing.T) (*tgbotapi.BotAPI, *atomic.Int64) {
	t.Helper()

	var polls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":42,"is_bot":true,"username":"beacon_test_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if polls.Add(1) == 1 {
				_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":7,"message":{"message_id":1,"date":1,"text":"hi","chat":{"id":99,"type":"private"}}}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
		}
	}))
	t.Cleanup(srv.Close)

	bot, err := tgbotapi.NewBotAPIWithClient("123456:test-token", srv.URL+"/bot%s/%s", srv.Client())
	require.NoError(t, err)
	bot.Debug = false
	return bot, &polls
}

// TestTelegramBotClient_Updates covers the half of the bot loop that shutdown
// depends on. Nothing else notices a poller that outlives its context: the
// binary simply does not exit, and only a hanging SIGTERM says so.
func TestTelegramBotClient_Updates(t *testing.T) {
	t.Parallel()

	t.Run("delivers what the Bot API returns", func(t *testing.T) {
		t.Parallel()

		bot, _ := newFakeBotAPI(t)
		tbot := &TelegramBotClient{bot: bot, logger: io.Discard}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		select {
		case update := <-tbot.Updates(ctx):
			assert.Equal(t, 7, update.UpdateID)
			require.NotNil(t, update.Message)
			assert.Equal(t, "hi", update.Message.Text)
		case <-time.After(5 * time.Second):
			t.Fatal("no update arrived")
		}
	})

	t.Run("closes the channel when the context is cancelled", func(t *testing.T) {
		t.Parallel()

		bot, _ := newFakeBotAPI(t)
		tbot := &TelegramBotClient{bot: bot, logger: io.Discard}

		ctx, cancel := context.WithCancel(t.Context())
		updates := tbot.Updates(ctx)
		// Drain the one update so the loop is parked on the poll, which is where a
		// cancellation actually has to be noticed.
		<-updates
		cancel()

		select {
		case _, ok := <-updates:
			assert.False(t, ok, "the channel must be closed, not merely idle")
		case <-time.After(5 * time.Second):
			t.Fatal("the channel stayed open after cancellation")
		}
	})

	t.Run("a consumer that stops reading does not pin the poller", func(t *testing.T) {
		t.Parallel()

		bot, polls := newFakeBotAPI(t)
		logged := &syncBuffer{}
		tbot := &TelegramBotClient{bot: bot, logger: logged}

		ctx, cancel := context.WithCancel(t.Context())
		_ = tbot.Updates(ctx)

		// Nothing ever receives. Once the API has been polled the loop holds an
		// update with nobody taking it, so a send that does not also watch ctx parks
		// there forever — and cancellation never reaches the loop at all.
		require.Eventually(t, func() bool { return polls.Load() > 0 }, 5*time.Second, 10*time.Millisecond,
			"the Bot API was never polled, so the loop never reached the send this checks")
		cancel()

		// The exit line is the only evidence available here: the channel cannot be
		// watched for closure without receiving from it, and receiving is exactly
		// what would let a leaking loop off the hook.
		require.Eventually(t, func() bool { return strings.Contains(logged.String(), "stopped listening") },
			5*time.Second, 10*time.Millisecond,
			"the poller outlived its context with a consumer that walked away")
	})
}

// syncBuffer is a writer safe to read while the loop under test is still writing
// to it from its own goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestBotHTTPTimeoutOutlastsThePoll guards a coupling that fails silently in one
// direction only.
//
// The bot's HTTP client serves every Bot API call, getUpdates included, and that
// one holds a connection open for updatePollTimeoutSeconds on purpose. A client
// deadline at or below the hold turns every long poll into a timeout: the bot
// stops receiving anything, retries, times out again, and the log shows only
// transport errors.
func TestBotHTTPTimeoutOutlastsThePoll(t *testing.T) {
	t.Parallel()

	assert.Greater(t, botHTTPTimeout, time.Duration(updatePollTimeoutSeconds)*time.Second,
		"the client deadline must outlast the poll hold, or getUpdates can never return normally")
}

// TestTelegramBotClient_UpdatePollTimeout keeps the long-poll hold time honest:
// it is the one number that decides how often an idle bot talks to Telegram.
func TestTelegramBotClient_UpdatePollTimeout(t *testing.T) {
	t.Parallel()

	assert.Positive(t, updatePollTimeoutSeconds,
		"a zero hold time turns long polling into a busy loop against the Bot API")
	assert.LessOrEqual(t, updatePollTimeoutSeconds, 60,
		"Telegram caps the hold at 60s; longer is silently clamped and only delays noticing a dropped connection")
}
