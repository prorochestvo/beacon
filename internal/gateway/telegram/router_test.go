package telegram

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	appdigest "github.com/seilbekskindirov/beacon/internal/application/digest"
	integration "github.com/seilbekskindirov/beacon/internal/infrastructure/telegrambot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ BotClient     = (*mockBot)(nil)
	_ DigestService = (*mockDigest)(nil)
	// The production client must keep satisfying the surface this router asks
	// for; nothing else would notice a method being renamed under it.
	_ BotClient = (*integration.TelegramBotClient)(nil)
)

const testChatID int64 = 123456789

// mockBot records every outbound call and can hand out a canned update stream.
// The keyboards slice is shared between the send and edit paths; editedMsgIDs is
// what discriminates them.
type mockBot struct {
	mu           sync.Mutex
	htmlMessages []string
	keyboards    []tgbotapi.InlineKeyboardMarkup
	answeredCBs  []string
	editedMsgIDs []int
	editedTexts  []string
	seen         int

	updates []tgbotapi.Update
	// endless keeps re-emitting updates until ctx is cancelled, so a test about
	// shutdown cannot pass by simply running out of canned input.
	endless bool
}

func (m *mockBot) Updates(ctx context.Context) <-chan tgbotapi.Update {
	out := make(chan tgbotapi.Update)
	go func() {
		defer close(out)
		for {
			for _, u := range m.updates {
				select {
				case out <- u:
					m.mu.Lock()
					m.seen++
					m.mu.Unlock()
				case <-ctx.Done():
					return
				}
			}
			if !m.endless {
				return
			}
		}
	}()
	return out
}

func (m *mockBot) SendHTMLMessage(_ context.Context, _ integration.TelegramChatID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.htmlMessages = append(m.htmlMessages, text)
	return nil
}

func (m *mockBot) SendHTMLMessageWithKeyboard(_ context.Context, _ integration.TelegramChatID, text string, kb tgbotapi.InlineKeyboardMarkup) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.htmlMessages = append(m.htmlMessages, text)
	m.keyboards = append(m.keyboards, kb)
	return nil
}

func (m *mockBot) EditHTMLMessageWithKeyboard(_ context.Context, _ integration.TelegramChatID, messageID int, text string, kb tgbotapi.InlineKeyboardMarkup) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.editedMsgIDs = append(m.editedMsgIDs, messageID)
	m.editedTexts = append(m.editedTexts, text)
	m.keyboards = append(m.keyboards, kb)
	return nil
}

func (m *mockBot) EditMessageText(_ context.Context, _ integration.TelegramChatID, messageID int, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.editedMsgIDs = append(m.editedMsgIDs, messageID)
	m.editedTexts = append(m.editedTexts, text)
	return nil
}

func (m *mockBot) AnswerCallbackQuery(_ context.Context, id, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answeredCBs = append(m.answeredCBs, id)
	return nil
}

// dispatched reports how many updates the router has taken off the channel,
// inferred from the acknowledgements and sends they produce.
func (m *mockBot) dispatched() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen
}

func (m *mockBot) sent() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.htmlMessages...)
}

// mockDigest is a test double for DigestService. It records the user id the
// router derived from the chat id, so the two staying in step is checked rather
// than assumed.
type mockDigest struct {
	digest appdigest.Digest
	err    error

	gotUserID string
}

func (m *mockDigest) ObtainMeDigest(_ context.Context, userID string) (appdigest.Digest, error) {
	m.gotUserID = userID
	if m.err != nil {
		return appdigest.Digest{}, m.err
	}
	return m.digest, nil
}

// newRouter builds a Router over the given doubles with logging discarded.
func newRouter(t *testing.T, bot BotClient, digest DigestService, webAppURL string) *Router {
	t.Helper()
	r, err := NewRouter(Config{
		Bot:       bot,
		Digest:    digest,
		WebAppURL: webAppURL,
		Logger:    log.New(io.Discard, "", 0),
	})
	require.NoError(t, err)
	return r
}

// message builds a minimal inbound message. The OvyFlash fork carries Chat by
// value, not by pointer.
func message(chatID int64, text string) *tgbotapi.Message {
	return &tgbotapi.Message{Chat: tgbotapi.Chat{ID: chatID}, Text: text}
}

// callback builds a minimal inline-keyboard press carrying data.
func callback(chatID int64, msgID int, data string) *tgbotapi.CallbackQuery {
	return &tgbotapi.CallbackQuery{
		ID:      "cb-1",
		Data:    data,
		Message: &tgbotapi.Message{Chat: tgbotapi.Chat{ID: chatID}, MessageID: msgID},
	}
}

func TestNewRouter(t *testing.T) {
	t.Parallel()

	t.Run("a complete config yields a router that needs nothing attached", func(t *testing.T) {
		t.Parallel()
		r, err := NewRouter(Config{Bot: &mockBot{}, Digest: &mockDigest{}})
		require.NoError(t, err)
		require.NotNil(t, r)
		assert.Same(t, log.Default(), r.logger, "a nil logger falls back to the standard one")
	})

	t.Run("each required dependency is rejected by name", func(t *testing.T) {
		t.Parallel()
		without := map[string]func(*Config){
			"Bot":    func(c *Config) { c.Bot = nil },
			"Digest": func(c *Config) { c.Digest = nil },
		}
		for name, drop := range without {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				cfg := Config{Bot: &mockBot{}, Digest: &mockDigest{}}
				drop(&cfg)
				r, err := NewRouter(cfg)
				require.Error(t, err)
				require.Nil(t, r)
				assert.Contains(t, err.Error(), name)
			})
		}
	})

	t.Run("an empty WebAppURL is a deployment choice, not a missing dependency", func(t *testing.T) {
		t.Parallel()
		_, err := NewRouter(Config{Bot: &mockBot{}, Digest: &mockDigest{}})
		require.NoError(t, err)
	})
}

// TestRouter_Run covers the seam the split exists for: the router consumes a
// channel it does not own, and stops when that channel closes.
func TestRouter_Run(t *testing.T) {
	t.Parallel()

	t.Run("dispatches every update and returns when the source closes", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{updates: []tgbotapi.Update{
			{UpdateID: 1, Message: message(testChatID, "/start")},
			{UpdateID: 2, CallbackQuery: callback(testChatID, 42, cbBack)},
			{UpdateID: 3}, // neither message nor callback: ignored, not fatal
		}}
		r := newRouter(t, bot, &mockDigest{}, "")

		// Run blocks until the source closes; the mock closes after its canned
		// updates, so a hang here is the test failing by timeout.
		r.Run(t.Context())

		assert.Len(t, bot.sent(), 1, "the message press sends a new menu")
		assert.Len(t, bot.editedMsgIDs, 1, "the callback press edits the existing bubble")
		assert.Equal(t, []string{"cb-1"}, bot.answeredCBs)
	})

	t.Run("stops when the context is cancelled", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		// A source that never runs dry, so nothing but cancellation can end this
		// loop — which is the property shutdown depends on. Updates the router
		// ignores keep the test off the send paths.
		bot := &mockBot{updates: []tgbotapi.Update{{UpdateID: 1}}, endless: true}
		r := newRouter(t, bot, &mockDigest{}, "")

		done := make(chan struct{})
		go func() {
			defer close(done)
			r.Run(ctx)
		}()

		// Let the loop actually get going before pulling the context, or the test
		// would pass on a Run that never started.
		require.Eventually(t, func() bool { return bot.dispatched() > 0 }, time.Second, time.Millisecond)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after the context was cancelled")
		}
	})
}

// TestRouter_handleMessage: any inbound message — known command, unknown slash
// command, or free text — produces one main-menu reply.
func TestRouter_handleMessage(t *testing.T) {
	t.Parallel()

	inbound := map[string]string{
		"the subscriptions command": "/subscriptions",
		"the start command":         "/start",
		"a padded, shouted command": "  /START  ",
		"an unknown slash command":  "/nope",
		"free-form text":            "hello there",
	}
	for name, text := range inbound {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			bot := &mockBot{}
			newRouter(t, bot, &mockDigest{}, "").handleMessage(t.Context(), message(testChatID, text))

			require.Len(t, bot.keyboards, 1, "every inbound message lands on the same keyboard")
			assert.Equal(t, textMainMenu, bot.sent()[0])
			assert.Empty(t, bot.editedMsgIDs, "a text command opens a new bubble")
		})
	}
}

func TestRouter_handleCallback(t *testing.T) {
	t.Parallel()

	t.Run("acknowledges before doing anything, including for a press it ignores", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		// Data from a button removed in an older chat bubble.
		newRouter(t, bot, &mockDigest{}, "").handleCallback(t.Context(), callback(testChatID, 7, "sub:gone"))

		assert.Equal(t, []string{"cb-1"}, bot.answeredCBs,
			"an unacknowledged press leaves the caller's spinner turning forever")
		assert.Empty(t, bot.sent())
		assert.Empty(t, bot.editedMsgIDs)
	})

	t.Run("back returns to the menu in the same bubble", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		newRouter(t, bot, &mockDigest{}, "").handleCallback(t.Context(), callback(testChatID, 42, cbBack))

		require.Len(t, bot.editedMsgIDs, 1)
		assert.Equal(t, 42, bot.editedMsgIDs[0])
		assert.Equal(t, textMainMenu, bot.editedTexts[0])
		assert.Empty(t, bot.sent(), "a callback edits rather than piling up bubbles")
	})

	t.Run("latest asks the digest for the pressing chat", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		digest := &mockDigest{digest: appdigest.Digest{Parts: []string{"<pre>rates</pre>"}, Subscriptions: 1, Priced: 1}}
		newRouter(t, bot, digest, "").handleCallback(t.Context(), callback(testChatID, 42, cbLatest))

		assert.Equal(t, "123456789", digest.gotUserID,
			"the digest is asked for the chat that pressed, never for anything in the callback data")
		require.Len(t, bot.editedTexts, 1)
		assert.Equal(t, "<pre>rates</pre>", bot.editedTexts[0])
	})
}

func TestRouter_sendMainMenu(t *testing.T) {
	t.Parallel()

	t.Run("edits in place when a message id is given", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		newRouter(t, bot, &mockDigest{}, "").sendMainMenu(t.Context(), testChatID, 42)

		require.Len(t, bot.editedMsgIDs, 1)
		assert.Equal(t, 42, bot.editedMsgIDs[0])
		assert.Empty(t, bot.sent(), "editing is what keeps a button press from growing the chat")
	})

	t.Run("sends a new message when there is none to edit", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		newRouter(t, bot, &mockDigest{}, "").sendMainMenu(t.Context(), testChatID, 0)

		require.Len(t, bot.keyboards, 1)
		assert.Empty(t, bot.editedMsgIDs)
	})

	t.Run("carries a WebApp launcher when an origin is configured", func(t *testing.T) {
		t.Parallel()

		const wantURL = "https://example.com/"
		bot := &mockBot{}
		newRouter(t, bot, &mockDigest{}, wantURL).sendMainMenu(t.Context(), testChatID, 0)

		require.Len(t, bot.keyboards, 1)
		kb := bot.keyboards[0].InlineKeyboard
		require.Len(t, kb, 2, "one row for Latest updates, one for the launcher")
		require.Len(t, kb[1], 1)
		require.NotNil(t, kb[1][0].WebApp,
			"a plain URL button opens a browser without initData, so the Mini App cannot authenticate")
		assert.Equal(t, wantURL, kb[1][0].WebApp.URL)
		assert.Empty(t, kb[1][0].URL, "a WebApp button must not also set the URL field")
	})

	t.Run("omits the launcher when no origin is configured", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		newRouter(t, bot, &mockDigest{}, "").sendMainMenu(t.Context(), testChatID, 0)

		require.Len(t, bot.keyboards, 1)
		assert.Len(t, bot.keyboards[0].InlineKeyboard, 1)
	})
}

// TestRouter_sendLatestUpdates covers the half of the split that stayed here:
// turning the digest's three outcomes into three different things to say.
func TestRouter_sendLatestUpdates(t *testing.T) {
	t.Parallel()

	t.Run("an empty account and an unpriced one are told apart", func(t *testing.T) {
		t.Parallel()

		outcomes := map[string]struct {
			digest appdigest.Digest
			says   string
		}{
			"no subscriptions at all": {appdigest.Digest{}, textNoSubscriptions},
			"subscriptions, nothing collected yet": {
				appdigest.Digest{Subscriptions: 3}, textNoRateData,
			},
		}
		for name, tc := range outcomes {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				bot := &mockBot{}
				newRouter(t, bot, &mockDigest{digest: tc.digest}, "").
					sendLatestUpdates(t.Context(), testChatID, 0)

				require.Len(t, bot.sent(), 1)
				assert.Equal(t, tc.says, bot.sent()[0],
					"an empty Parts alone cannot tell these apart; the counts are what do")
				require.Len(t, bot.keyboards, 1, "both states still offer a way back")
			})
		}
	})

	t.Run("a failure warns the user and records why", func(t *testing.T) {
		t.Parallel()

		var logged strings.Builder
		r, err := NewRouter(Config{
			Bot:    &mockBot{},
			Digest: &mockDigest{err: errors.New("db down")},
			Logger: log.New(&logged, "", 0),
		})
		require.NoError(t, err)
		bot, ok := r.bot.(*mockBot)
		require.True(t, ok)

		r.sendLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, bot.sent(), 1)
		assert.Equal(t, textDigestFailed, bot.sent()[0])
		assert.Empty(t, bot.keyboards, "the warning is a plain message, not a menu")
		assert.Contains(t, logged.String(), "db down",
			"a dead repository must not look the same in the log as an empty account")
	})

	t.Run("a single part goes into the bubble that asked for it", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		digest := &mockDigest{digest: appdigest.Digest{Parts: []string{"part one"}, Subscriptions: 1, Priced: 1}}
		newRouter(t, bot, digest, "").sendLatestUpdates(t.Context(), testChatID, 42)

		require.Len(t, bot.editedTexts, 1)
		assert.Equal(t, "part one", bot.editedTexts[0])
		require.Len(t, bot.keyboards, 1)
	})
}

// TestRouter_sendDigestParts pins the multi-part delivery. The test this replaces
// asserted "exactly one keyboard" against a fixture that never produced a second
// part, so the branch below ran unchecked.
func TestRouter_sendDigestParts(t *testing.T) {
	t.Parallel()

	parts := []string{"part one", "part two", "part three"}

	t.Run("edits the asking bubble, then sends the rest, keyboard on the last", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		newRouter(t, bot, &mockDigest{}, "").sendDigestParts(t.Context(), testChatID, 42, parts)

		require.Len(t, bot.editedTexts, 1)
		assert.Equal(t, "part one", bot.editedTexts[0])
		assert.Equal(t, 42, bot.editedMsgIDs[0])
		assert.Equal(t, []string{"part two", "part three"}, bot.sent())
		require.Len(t, bot.keyboards, 1, "one keyboard, or the user gets a Back button under every fragment")
	})

	t.Run("sends every part when there is no bubble to edit", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		newRouter(t, bot, &mockDigest{}, "").sendDigestParts(t.Context(), testChatID, 0, parts)

		assert.Equal(t, parts, bot.sent())
		assert.Empty(t, bot.editedMsgIDs)
		require.Len(t, bot.keyboards, 1)
	})

	t.Run("no parts sends nothing", func(t *testing.T) {
		t.Parallel()

		bot := &mockBot{}
		newRouter(t, bot, &mockDigest{}, "").sendDigestParts(t.Context(), testChatID, 42, nil)

		assert.Empty(t, bot.sent())
		assert.Empty(t, bot.editedMsgIDs)
	})
}
