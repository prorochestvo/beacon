package service

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/seilbekskindirov/monitor/internal/application/rulegen"
	"github.com/seilbekskindirov/monitor/internal/domain"
	integration "github.com/seilbekskindirov/monitor/internal/infrastructure/telegrambot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface checks.
var _ rulegenGenerator = (*fakeGenerator)(nil)
var _ telegramClient = (*fakeTelegramClient)(nil)

// fakeGenerator is a test double for rulegenGenerator.
type fakeGenerator struct {
	result *rulegen.Result
	err    error
	mu     sync.Mutex
	called bool
	done   chan struct{} // closed when Generate returns
}

func newFakeGenerator(result *rulegen.Result, err error) *fakeGenerator {
	return &fakeGenerator{
		result: result,
		err:    err,
		done:   make(chan struct{}),
	}
}

func (f *fakeGenerator) Generate(_ context.Context, _ string, _ bool) (*rulegen.Result, error) {
	f.mu.Lock()
	f.called = true
	f.mu.Unlock()
	close(f.done)
	return f.result, f.err
}

// fakeTelegramClient records calls to SendHTMLMessage, SendHTMLMessageReturning,
// and EditMessageText. The other methods are no-ops.
type fakeTelegramClient struct {
	mu sync.Mutex

	// htmlMessages records all SendHTMLMessage calls.
	htmlMessages []string

	// sendReturnID is the message id returned by SendHTMLMessageReturning.
	sendReturnID  int
	sendReturnErr error

	// sendReturningCalls records the text passed to SendHTMLMessageReturning.
	sendReturningCalls []string

	// editCalls records (messageID, text) pairs passed to EditMessageText.
	editCalls []editCall
	editErr   error

	// editDone is closed on the first EditMessageText call, allowing tests to
	// wait for the goroutine to reach that point without time.Sleep.
	editDone chan struct{}
	editOnce sync.Once

	// keyboards is recorded but not asserted in regen tests.
	keyboards []tgbotapi.InlineKeyboardMarkup
}

func newFakeTelegramClient() *fakeTelegramClient {
	return &fakeTelegramClient{
		editDone: make(chan struct{}),
	}
}

func newFakeTelegramClientWithSendID(id int) *fakeTelegramClient {
	c := newFakeTelegramClient()
	c.sendReturnID = id
	return c
}

func newFakeTelegramClientWithSendErr(err error) *fakeTelegramClient {
	c := newFakeTelegramClient()
	c.sendReturnErr = err
	return c
}

type editCall struct {
	messageID int
	text      string
}

func (f *fakeTelegramClient) Listen(_ context.Context, _ integration.UpdateHandler) {}

func (f *fakeTelegramClient) SendPlainTextMessage(_ context.Context, _ integration.TelegramChatID, _ string) error {
	return nil
}

func (f *fakeTelegramClient) SendMarkdownMessage(_ context.Context, _ integration.TelegramChatID, _ string) error {
	return nil
}

func (f *fakeTelegramClient) SendHTMLMessage(_ context.Context, _ integration.TelegramChatID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.htmlMessages = append(f.htmlMessages, text)
	return nil
}

func (f *fakeTelegramClient) SendHTMLMessageReturning(_ context.Context, _ integration.TelegramChatID, text string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendReturningCalls = append(f.sendReturningCalls, text)
	return f.sendReturnID, f.sendReturnErr
}

func (f *fakeTelegramClient) SendHTMLMessageWithKeyboard(_ context.Context, _ integration.TelegramChatID, text string, kb tgbotapi.InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.htmlMessages = append(f.htmlMessages, text)
	f.keyboards = append(f.keyboards, kb)
	return nil
}

func (f *fakeTelegramClient) EditHTMLMessageWithKeyboard(_ context.Context, _ integration.TelegramChatID, _ int, _ string, kb tgbotapi.InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keyboards = append(f.keyboards, kb)
	return nil
}

func (f *fakeTelegramClient) EditMessageText(_ context.Context, _ integration.TelegramChatID, messageID int, text string) error {
	f.mu.Lock()
	f.editCalls = append(f.editCalls, editCall{messageID: messageID, text: text})
	editErr := f.editErr
	f.mu.Unlock()
	// Signal the first edit so tests can deterministically wait for the goroutine.
	f.editOnce.Do(func() { close(f.editDone) })
	return editErr
}

func (f *fakeTelegramClient) AnswerCallbackQuery(_ context.Context, _ string, _ string) error {
	return nil
}

// cannedRegenResult builds a valid Result for happy-path tests.
func cannedRegenResult(sourceName string) *rulegen.Result {
	src := &domain.RateSource{
		Name:          sourceName,
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
	}
	return &rulegen.Result{
		Source: src,
		Rules: []domain.RateSourceRule{
			{Method: domain.MethodRegex, Pattern: `(\d+\.\d+)`},
		},
		Metadata: domain.RateSourceRuleMetadata{
			Provider:    "OpenAI",
			Model:       "gpt-4o",
			GeneratedAt: "2026-05-18T10:00:00Z",
		},
		Value:        467.95,
		AttemptsUsed: 2,
		Escalated:    false,
	}
}

// newRegenApi builds a TelegramApi with the given generator and admin id for regen tests.
func newRegenApi(
	t *testing.T,
	client *fakeTelegramClient,
	gen rulegenGenerator,
	adminChatID int64,
	factory func(maxPrimary, maxFallback int) (rulegenGenerator, error),
) *TelegramApi {
	t.Helper()
	locks := rulegen.NewLockManager()
	var locksArg *rulegen.LockManager
	if gen != nil {
		locksArg = locks
	}
	h, err := NewTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{}, "", gen, locksArg, adminChatID, factory)
	require.NoError(t, err)
	return h
}

// buildRegenMsg builds a *tgbotapi.Message for /regen tests.
func buildRegenMsg(chatID int64, text string) *tgbotapi.Message {
	return &tgbotapi.Message{
		Chat: tgbotapi.Chat{ID: chatID},
		Text: text,
	}
}

const (
	regenAdminChatID    int64 = 999
	regenNonAdminChatID int64 = 111
	testSourceName            = "KZ_QAZPOST_BID_USD_KZT"
)

func TestParseRegenCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		input         string
		wantSource    string
		wantForce     bool
		wantMaxFallbk int
		wantErr       error
	}{
		{
			name:       "source only",
			input:      "/regen KZ_QAZPOST_BID_USD_KZT",
			wantSource: "KZ_QAZPOST_BID_USD_KZT",
		},
		{
			name:       "source with force-fallback",
			input:      "/regen KZ_QAZPOST_BID_USD_KZT --force-fallback",
			wantSource: "KZ_QAZPOST_BID_USD_KZT",
			wantForce:  true,
		},
		{
			name:          "source with max-fallback",
			input:         "/regen KZ_QAZPOST_BID_USD_KZT --max-fallback=4",
			wantSource:    "KZ_QAZPOST_BID_USD_KZT",
			wantMaxFallbk: 4,
		},
		{
			name:          "source with both flags",
			input:         "/regen KZ_QAZPOST_BID_USD_KZT --force-fallback --max-fallback=2",
			wantSource:    "KZ_QAZPOST_BID_USD_KZT",
			wantForce:     true,
			wantMaxFallbk: 2,
		},
		{
			name:    "no source name",
			input:   "/regen",
			wantErr: errMissingSourceName,
		},
		{
			name:    "two positional args",
			input:   "/regen X Y",
			wantErr: errTooManyPositionalArgs,
		},
		{
			name:    "unknown flag",
			input:   "/regen X --unknown",
			wantErr: errUnknownFlag,
		},
		{
			name:    "max-fallback non-integer",
			input:   "/regen X --max-fallback=abc",
			wantErr: errInvalidMaxFallback,
		},
		{
			name:    "max-fallback zero is invalid",
			input:   "/regen X --max-fallback=0",
			wantErr: errInvalidMaxFallback,
		},
		{
			name:    "max-fallback 11 is out of range",
			input:   "/regen X --max-fallback=11",
			wantErr: errInvalidMaxFallback,
		},
		{
			name:       "uppercase command accepted",
			input:      "/REGEN X",
			wantSource: "X",
		},
		{
			name:       "botname suffix stripped",
			input:      "/regen@my_bot KZ_QAZPOST_BID_USD_KZT",
			wantSource: "KZ_QAZPOST_BID_USD_KZT",
		},
		{
			name:    "max-fallback empty value",
			input:   "/regen X --max-fallback=",
			wantErr: errInvalidMaxFallback,
		},
		{
			name:          "max-fallback boundary lower 1",
			input:         "/regen X --max-fallback=1",
			wantSource:    "X",
			wantMaxFallbk: 1,
		},
		{
			name:          "max-fallback boundary upper 10",
			input:         "/regen X --max-fallback=10",
			wantSource:    "X",
			wantMaxFallbk: 10,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotSource, gotForce, gotMax, gotErr := parseRegenCommand(tc.input)
			if tc.wantErr != nil {
				require.Error(t, gotErr)
				assert.True(t, errors.Is(gotErr, tc.wantErr),
					"expected %v, got %v", tc.wantErr, gotErr)
				assert.Empty(t, gotSource)
				assert.False(t, gotForce)
				assert.Zero(t, gotMax)
			} else {
				require.NoError(t, gotErr)
				assert.Equal(t, tc.wantSource, gotSource)
				assert.Equal(t, tc.wantForce, gotForce)
				assert.Equal(t, tc.wantMaxFallbk, gotMax)
			}
		})
	}
}

func TestTelegramApi_HandleRegen(t *testing.T) {
	t.Parallel()

	t.Run("non-admin sender is ignored", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClient()
		h := newRegenApi(t, client, newFakeGenerator(cannedRegenResult(testSourceName), nil), regenAdminChatID, nil)

		h.handleRegen(t.Context(), buildRegenMsg(regenNonAdminChatID, "/regen "+testSourceName))

		client.mu.Lock()
		defer client.mu.Unlock()
		assert.Empty(t, client.htmlMessages, "non-admin should receive no reply")
		assert.Empty(t, client.sendReturningCalls, "no working message for non-admin")
		assert.Empty(t, client.editCalls, "no edit for non-admin")
	})

	t.Run("regen disabled replies politely", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClient()
		// gen=nil means regen disabled; locks must also be nil per constructor guard.
		h, err := NewTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{}, "", nil, nil, regenAdminChatID, nil)
		require.NoError(t, err)

		h.handleRegen(t.Context(), buildRegenMsg(regenAdminChatID, "/regen "+testSourceName))

		client.mu.Lock()
		defer client.mu.Unlock()
		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "not enabled")
	})

	t.Run("parse error replies usage", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClient()
		h := newRegenApi(t, client, newFakeGenerator(nil, nil), regenAdminChatID, nil)

		// "/regen" with no source name triggers errMissingSourceName.
		h.handleRegen(t.Context(), buildRegenMsg(regenAdminChatID, "/regen"))

		client.mu.Lock()
		defer client.mu.Unlock()
		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "Usage")
	})

	t.Run("lock contention replies", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClient()
		gen := newFakeGenerator(cannedRegenResult(testSourceName), nil)
		locks := rulegen.NewLockManager()
		h, err := NewTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{}, "", gen, locks, regenAdminChatID, nil)
		require.NoError(t, err)

		// Pre-acquire the lock so the handler sees contention.
		_, ok := locks.TryAcquire(testSourceName)
		require.True(t, ok, "pre-acquire must succeed")

		h.handleRegen(t.Context(), buildRegenMsg(regenAdminChatID, "/regen "+testSourceName))

		client.mu.Lock()
		defer client.mu.Unlock()
		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "already in progress")
		assert.Contains(t, client.htmlMessages[0], testSourceName)
	})

	t.Run("success path edits the working message", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClientWithSendID(42)
		result := cannedRegenResult(testSourceName)
		gen := newFakeGenerator(result, nil)
		h := newRegenApi(t, client, gen, regenAdminChatID, nil)

		h.handleRegen(t.Context(), buildRegenMsg(regenAdminChatID, "/regen "+testSourceName))

		// Wait for EditMessageText to be called (goroutine completion signal).
		<-client.editDone

		client.mu.Lock()
		defer client.mu.Unlock()

		require.Len(t, client.sendReturningCalls, 1, "one working message expected")
		assert.Contains(t, client.sendReturningCalls[0], testSourceName)

		require.Len(t, client.editCalls, 1, "one edit expected")
		assert.Equal(t, 42, client.editCalls[0].messageID, "edited message id must match working msg id")
		finalText := client.editCalls[0].text
		assert.Contains(t, finalText, "✅")
		assert.Contains(t, finalText, testSourceName)
		assert.Contains(t, finalText, "OpenAI")
		assert.Contains(t, finalText, "gpt-4o")
		assert.Contains(t, finalText, "2") // AttemptsUsed
	})

	t.Run("attempts-exhausted path edits with failure body", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClientWithSendID(55)
		gen := newFakeGenerator(nil, fmt.Errorf("wrap: %w", rulegen.ErrAttemptsExhausted))
		h := newRegenApi(t, client, gen, regenAdminChatID, nil)

		h.handleRegen(t.Context(), buildRegenMsg(regenAdminChatID, "/regen "+testSourceName))
		<-client.editDone

		client.mu.Lock()
		defer client.mu.Unlock()
		require.Len(t, client.editCalls, 1)
		assert.Contains(t, client.editCalls[0].text, "❌")
		assert.Contains(t, client.editCalls[0].text, "all attempts exhausted")
	})

	t.Run("source-not-found path", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClientWithSendID(56)
		gen := newFakeGenerator(nil, fmt.Errorf("wrap: %w", rulegen.ErrSourceNotFound))
		h := newRegenApi(t, client, gen, regenAdminChatID, nil)

		h.handleRegen(t.Context(), buildRegenMsg(regenAdminChatID, "/regen "+testSourceName))
		<-client.editDone

		client.mu.Lock()
		defer client.mu.Unlock()
		require.Len(t, client.editCalls, 1)
		assert.Contains(t, client.editCalls[0].text, "source not found")
	})

	t.Run("unsupported fetcher path", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClientWithSendID(57)
		gen := newFakeGenerator(nil, fmt.Errorf("wrap: %w", rulegen.ErrUnsupportedFetcherKind))
		h := newRegenApi(t, client, gen, regenAdminChatID, nil)

		h.handleRegen(t.Context(), buildRegenMsg(regenAdminChatID, "/regen "+testSourceName))
		<-client.editDone

		client.mu.Lock()
		defer client.mu.Unlock()
		require.Len(t, client.editCalls, 1)
		assert.Contains(t, client.editCalls[0].text, "required fetcher not available")
	})

	t.Run("working-message send failure aborts cleanly", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClientWithSendErr(errors.New("network error"))
		gen := newFakeGenerator(cannedRegenResult(testSourceName), nil)
		locks := rulegen.NewLockManager()
		h, err := NewTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{}, "", gen, locks, regenAdminChatID, nil)
		require.NoError(t, err)

		h.handleRegen(t.Context(), buildRegenMsg(regenAdminChatID, "/regen "+testSourceName))

		// handleRegen returns synchronously after the send failure (no goroutine spawned),
		// so we can assert immediately — no synchronisation needed.

		// Generator must NOT have been called because we aborted.
		gen.mu.Lock()
		wasCalled := gen.called
		gen.mu.Unlock()
		assert.False(t, wasCalled, "generator must not be called when working message send fails")

		// Lock must be released — we should be able to acquire it again.
		releaseAgain, acquired := locks.TryAcquire(testSourceName)
		assert.True(t, acquired, "lock must be released after send failure")
		if acquired {
			releaseAgain()
		}
	})

	t.Run("lock is released on success", func(t *testing.T) {
		t.Parallel()
		client := newFakeTelegramClientWithSendID(77)
		result := cannedRegenResult(testSourceName)
		gen := newFakeGenerator(result, nil)
		locks := rulegen.NewLockManager()
		h, err := NewTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{}, "", gen, locks, regenAdminChatID, nil)
		require.NoError(t, err)

		h.handleRegen(t.Context(), buildRegenMsg(regenAdminChatID, "/regen "+testSourceName))

		// Wait for EditMessageText to be called; the goroutine's deferred release()
		// fires immediately after EditMessageText returns, so by the time we observe
		// editDone the lock is guaranteed to be (or about to be) released. We use a
		// small busy-poll to handle the few-nanosecond window between editDone closing
		// and release() executing.
		<-client.editDone

		// Lock must be freed — a second TryAcquire must succeed.
		// Retry briefly to account for the nanosecond gap between EditMessageText
		// returning and the deferred release() executing. Gosched() yields the
		// scheduler so the goroutine running release() gets a chance to run.
		var acquired bool
		var release func()
		for range 10_000 {
			release, acquired = locks.TryAcquire(testSourceName)
			if acquired {
				break
			}
			runtime.Gosched()
		}
		assert.True(t, acquired, "lock must be released after successful generate")
		if acquired {
			release()
		}
	})
}
