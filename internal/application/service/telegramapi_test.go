package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/domain"
	integration "github.com/seilbekskindirov/monitor/internal/infrastructure/telegrambot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramApi_handleLatestUpdates(t *testing.T) {
	t.Parallel()

	bidSub := func(sourceName string, latestRate float64) domain.RateUserSubscription {
		return domain.RateUserSubscription{
			UserType:           domain.UserTypeTelegram,
			UserID:             fmt.Sprintf("%d", testChatID),
			SourceName:         sourceName,
			ConditionType:      domain.ConditionTypeDelta,
			ConditionValue:     "5",
			LatestNotifiedRate: latestRate,
		}
	}

	bidSource := func(name string) domain.RateSource {
		return domain.RateSource{
			Name:          name,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Kind:          domain.RateSourceKindBID,
		}
	}

	t.Run("shows table row for subscription with non-zero LatestNotifiedRate", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sub := bidSub("src1", 480)
		src := bidSource("src1")
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			&mockRateValueRepo{prices: map[string]float64{"src1": 487.55}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"src1": src}},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		msg := client.htmlMessages[0]
		assert.Contains(t, msg, "<pre>")
		assert.Contains(t, msg, "USD/KZT")
		assert.Contains(t, msg, "FX rates")
		assert.NotContains(t, msg, "#DELTA")
		assert.NotContains(t, msg, "#DAILY")
		assert.NotContains(t, msg, "#CRON")
		assert.NotContains(t, msg, "#INTERVAL")
	})

	t.Run("shows row with blank delta when LatestNotifiedRate is zero (first-fire guard)", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sub := bidSub("src1", 0) // first-fire: LatestNotifiedRate == 0
		src := bidSource("src1")
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			&mockRateValueRepo{prices: map[string]float64{"src1": 487.55}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"src1": src}},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		msg := client.htmlMessages[0]
		// Price must be present but no directional arrow (delta == CurrentPrice guard).
		assert.Contains(t, msg, "487.55")
		assert.NotContains(t, msg, "↑")
		assert.NotContains(t, msg, "↓")
	})

	t.Run("shows empty-state when user has no subscriptions", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{}},
			&mockRateValueRepo{},
			&mockRateSourceRepo{},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Contains(t, client.htmlMessages[0], "no subscriptions yet")
	})

	t.Run("shows error message when repo fails", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(
			client,
			&mockSubRepo{err: fmt.Errorf("db error")},
			&mockRateValueRepo{},
			&mockRateSourceRepo{},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "⚠️")
	})

	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sub := bidSub("src1", 480)
		src := bidSource("src1")
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			&mockRateValueRepo{prices: map[string]float64{"src1": 487.55}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"src1": src}},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 88)

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 88, client.editedMsgIDs[0])
		// Single part → EditHTMLMessageWithKeyboard, no plain htmlMessages.
		assert.Empty(t, client.htmlMessages)
	})

	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sub := bidSub("src1", 480)
		src := bidSource("src1")
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			&mockRateValueRepo{prices: map[string]float64{"src1": 487.55}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"src1": src}},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})

	t.Run("filters out subscriptions whose source has no current rate", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sub := bidSub("src1", 480)
		src := bidSource("src1")
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			// Rate value repo returns nothing for src1.
			&mockRateValueRepo{prices: map[string]float64{}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"src1": src}},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		// All subs filtered → "No rate data available yet." message.
		require.Len(t, client.keyboards, 1)
		assert.Contains(t, client.htmlMessages[0], "No rate data available yet.")
		assert.NotContains(t, client.htmlMessages[0], "no subscriptions yet")
	})

	t.Run("filters out subscriptions whose source row is missing", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		// Sub references "GHOST" which is absent from the source map.
		sub := bidSub("GHOST", 480)
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			&mockRateValueRepo{prices: map[string]float64{"GHOST": 487}},
			// Source repo has no entry for "GHOST".
			&mockRateSourceRepo{sources: map[string]domain.RateSource{}},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Contains(t, client.htmlMessages[0], "No rate data available yet.")
	})

	t.Run("renders cross-source dedup row keeping BID MAX price", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		highSub := bidSub("S_HIGH", 0)
		lowSub := bidSub("S_LOW", 0)
		// Both subs share (USD, KZT, BID) — should produce a single row.
		srcHigh := bidSource("S_HIGH")
		srcLow := bidSource("S_LOW")
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{highSub, lowSub}},
			&mockRateValueRepo{prices: map[string]float64{"S_HIGH": 490, "S_LOW": 488}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"S_HIGH": srcHigh, "S_LOW": srcLow}},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		msg := client.htmlMessages[0]
		// One row for USD/KZT; BID-MAX means 490 wins over 488.
		assert.Equal(t, 1, strings.Count(msg, "USD/KZT"))
		assert.Contains(t, msg, "490")
		assert.NotContains(t, msg, "488", "BID-MAX: loser price must be absent")
	})

	t.Run("uses user's timezone when profile is present", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sub := bidSub("src1", 480)
		src := bidSource("src1")
		profileRepo := &mockRateUserProfileRepo{
			profile: &domain.RateUserProfile{
				Timezone: "Asia/Almaty",
			},
		}
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			&mockRateValueRepo{prices: map[string]float64{"src1": 487.55}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"src1": src}},
			profileRepo,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		// Asia/Almaty is UTC+5; timestamp should contain +05.
		assert.Contains(t, client.htmlMessages[0], "+05")
	})

	t.Run("falls back to UTC when profile repo is nil", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sub := bidSub("src1", 480)
		src := bidSource("src1")
		// nil profileRepo → UTC fallback, no panic.
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			&mockRateValueRepo{prices: map[string]float64{"src1": 487.55}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"src1": src}},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Contains(t, client.htmlMessages[0], "+00")
	})

	t.Run("falls back to UTC when profile lookup returns ErrNotFound", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sub := bidSub("src1", 480)
		src := bidSource("src1")
		profileRepo := &mockRateUserProfileRepo{err: internal.ErrNotFound}
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			&mockRateValueRepo{prices: map[string]float64{"src1": 487.55}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"src1": src}},
			profileRepo,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Contains(t, client.htmlMessages[0], "+00")
	})

	t.Run("falls back to UTC when profile timezone is unrecognisable", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sub := bidSub("src1", 480)
		src := bidSource("src1")
		profileRepo := &mockRateUserProfileRepo{
			profile: &domain.RateUserProfile{
				Timezone: "Not/AReal/Zone",
			},
		}
		h := newTelegramApi(
			client,
			&mockSubRepo{subs: []domain.RateUserSubscription{sub}},
			&mockRateValueRepo{prices: map[string]float64{"src1": 487.55}},
			&mockRateSourceRepo{sources: map[string]domain.RateSource{"src1": src}},
			profileRepo,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		// Unknown timezone causes LoadLocation to fail → UTC fallback → "+00" in timestamp.
		assert.Contains(t, client.htmlMessages[0], "+00")
	})

	t.Run("splits into multiple parts when output exceeds telegramMaxMessageLen", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}

		// Build enough unique subs to force multi-part output. Using distinct
		// (base, quote, kind) so dedup does not collapse them.
		const subCount = 60
		subs := make([]domain.RateUserSubscription, subCount)
		sources := make(map[string]domain.RateSource, subCount)
		prices := make(map[string]float64, subCount)
		for i := 0; i < subCount; i++ {
			name := fmt.Sprintf("source_%03d", i)
			// Give each source a unique quote currency so they produce distinct rows.
			quoteCurrency := fmt.Sprintf("XX%d", i)
			subs[i] = domain.RateUserSubscription{
				UserType:           domain.UserTypeTelegram,
				UserID:             fmt.Sprintf("%d", testChatID),
				SourceName:         name,
				ConditionType:      domain.ConditionTypeDelta,
				ConditionValue:     "5",
				LatestNotifiedRate: 480,
			}
			sources[name] = domain.RateSource{
				Name:          name,
				BaseCurrency:  "USD",
				QuoteCurrency: quoteCurrency,
				Kind:          domain.RateSourceKindBID,
			}
			prices[name] = 487.55
		}

		h := newTelegramApi(
			client,
			&mockSubRepo{subs: subs},
			&mockRateValueRepo{prices: prices},
			&mockRateSourceRepo{sources: sources},
			nil,
		)

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		// Multi-part: total messages > 1; keyboard attaches only to the last part.
		totalMsgs := len(client.htmlMessages) + len(client.keyboards)
		assert.Greater(t, totalMsgs, 1, "must produce multiple parts for heavy users")
		// Exactly one keyboard — attached to the final part only.
		assert.Len(t, client.keyboards, 1, "keyboard must be attached to the last part only")
		for _, part := range client.htmlMessages {
			assert.Contains(t, part, "<pre>", "every part must contain a rendered table block")
		}
	})
}

// TestTelegramApi_handleMessage verifies any inbound message — known command,
// unknown slash command, or free text — produces a single main-menu keyboard reply.
func TestTelegramApi_handleMessage(t *testing.T) {
	t.Parallel()
	t.Run("sends main menu on /subscriptions command", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockRateValueRepo{}, &mockRateSourceRepo{}, nil)
		msg := buildMessage(testChatID, "/subscriptions")

		h.handleMessage(t.Context(), msg)

		require.Len(t, client.keyboards, 1, "main menu keyboard must be sent")
	})
	t.Run("sends main menu on /start command", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockRateValueRepo{}, &mockRateSourceRepo{}, nil)
		msg := buildMessage(testChatID, "/start")

		h.handleMessage(t.Context(), msg)

		require.Len(t, client.keyboards, 1)
	})
	t.Run("sends main menu on case-insensitive and padded command", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockRateValueRepo{}, &mockRateSourceRepo{}, nil)
		msg := buildMessage(testChatID, "  /SUBSCRIPTIONS  ")

		h.handleMessage(t.Context(), msg)

		require.Len(t, client.keyboards, 1)
	})
	t.Run("sends main menu on free-form text", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockRateValueRepo{}, &mockRateSourceRepo{}, nil)
		msg := buildMessage(testChatID, "hello")

		h.handleMessage(t.Context(), msg)

		require.Len(t, client.keyboards, 1, "any inbound text must produce the main-menu keyboard")
		require.Len(t, client.htmlMessages, 1, "exactly one outbound message — the menu — no separate hint")
		assert.NotContains(t, client.htmlMessages[0], "Please use",
			"the old /subscriptions hint must not be sent anymore")
	})
}

func TestTelegramApi_sendMainMenu(t *testing.T) {
	t.Parallel()
	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockRateValueRepo{}, &mockRateSourceRepo{}, nil)

		h.sendMainMenu(t.Context(), testChatID, 42)

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 42, client.editedMsgIDs[0])
		assert.Empty(t, client.htmlMessages, "must not send a new message")
	})
	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockRateValueRepo{}, &mockRateSourceRepo{}, nil)

		h.sendMainMenu(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})
	t.Run("keyboard contains Mini App WebApp button when webAppURL is set", func(t *testing.T) {
		t.Parallel()
		const wantURL = "https://example.com/"
		client := &mockTelegramClient{}
		h := newTelegramApiWithWebApp(client, &mockSubRepo{}, wantURL)

		h.sendMainMenu(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		kb := client.keyboards[0].InlineKeyboard
		// 1 Latest-updates row + 1 Mini App WebApp row = 2 rows total.
		require.Len(t, kb, 2)
		webAppRow := kb[1]
		require.Len(t, webAppRow, 1)
		require.NotNil(t, webAppRow[0].WebApp, "last row must carry a WebApp button, not a URL button")
		assert.Equal(t, wantURL, webAppRow[0].WebApp.URL)
		assert.Empty(t, webAppRow[0].URL, "WebApp button must not set the URL field")
	})
	t.Run("keyboard has no Mini App button when webAppURL is empty", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockRateValueRepo{}, &mockRateSourceRepo{}, nil)

		h.sendMainMenu(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		kb := client.keyboards[0].InlineKeyboard
		// Only the Latest-updates row remains when no WebApp URL is configured.
		require.Len(t, kb, 1)
	})
}

// newTelegramApi is a helper that creates a TelegramApi wired to the provided mocks.
func newTelegramApi(
	client *mockTelegramClient,
	subRepo subscriptionRepository,
	rateValueRepo telegramRateValueRepository,
	sourceRepo telegramRateSourceRepository,
	profileRepo rateUserProfileRepository,
) *TelegramApi {
	h, _ := NewTelegramApi(client, subRepo, rateValueRepo, sourceRepo, profileRepo, "")
	return h
}

// newTelegramApiWithWebApp creates a TelegramApi with a non-empty webAppURL for WebApp button tests.
func newTelegramApiWithWebApp(client *mockTelegramClient, subRepo subscriptionRepository, webAppURL string) *TelegramApi {
	h, _ := NewTelegramApi(client, subRepo, &mockRateValueRepo{}, &mockRateSourceRepo{}, nil, webAppURL)
	return h
}

// buildMessage constructs a minimal *tgbotapi.Message for testing handleMessage.
// The OvyFlash fork changed Message.Chat from *Chat to Chat (value).
func buildMessage(chatID int64, text string) *tgbotapi.Message {
	return &tgbotapi.Message{
		Chat: tgbotapi.Chat{ID: chatID},
		Text: text,
	}
}

// mockTelegramClient records all outbound messages and keyboards for assertion.
// The keyboards slice is shared between SendHTMLMessageWithKeyboard and
// EditHTMLMessageWithKeyboard; use editedMsgIDs length to discriminate.
type mockTelegramClient struct {
	mu           sync.Mutex
	htmlMessages []string
	keyboards    []tgbotapi.InlineKeyboardMarkup
	answeredCBs  []string
	editedMsgIDs []int    // tracks messageID of each Edit* call
	editedTexts  []string // tracks text of each Edit* call
}

func (m *mockTelegramClient) Listen(_ context.Context, _ integration.UpdateHandler) {}

func (m *mockTelegramClient) SendPlainTextMessage(_ context.Context, _ integration.TelegramChatID, _ string) error {
	return nil
}

func (m *mockTelegramClient) SendMarkdownMessage(_ context.Context, _ integration.TelegramChatID, _ string) error {
	return nil
}

func (m *mockTelegramClient) SendHTMLMessage(_ context.Context, _ integration.TelegramChatID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.htmlMessages = append(m.htmlMessages, text)
	return nil
}

func (m *mockTelegramClient) SendHTMLMessageWithKeyboard(_ context.Context, _ integration.TelegramChatID, text string, kb tgbotapi.InlineKeyboardMarkup) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.htmlMessages = append(m.htmlMessages, text)
	m.keyboards = append(m.keyboards, kb)
	return nil
}

func (m *mockTelegramClient) EditHTMLMessageWithKeyboard(_ context.Context, _ integration.TelegramChatID, messageID int, text string, kb tgbotapi.InlineKeyboardMarkup) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.editedMsgIDs = append(m.editedMsgIDs, messageID)
	m.editedTexts = append(m.editedTexts, text)
	m.keyboards = append(m.keyboards, kb)
	return nil
}

func (m *mockTelegramClient) SendHTMLMessageReturning(_ context.Context, _ integration.TelegramChatID, text string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.htmlMessages = append(m.htmlMessages, text)
	return 0, nil
}

func (m *mockTelegramClient) EditMessageText(_ context.Context, _ integration.TelegramChatID, messageID int, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.editedMsgIDs = append(m.editedMsgIDs, messageID)
	m.editedTexts = append(m.editedTexts, text)
	return nil
}

func (m *mockTelegramClient) AnswerCallbackQuery(_ context.Context, id, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answeredCBs = append(m.answeredCBs, id)
	return nil
}

// mockSubRepo is a test double for subscriptionRepository.
type mockSubRepo struct {
	subs []domain.RateUserSubscription
	err  error
}

func (m *mockSubRepo) ObtainRateUserSubscriptionsByUserID(_ context.Context, _ domain.UserType, _ string) ([]domain.RateUserSubscription, error) {
	return m.subs, m.err
}

// mockRateValueRepo is a test double for telegramRateValueRepository.
type mockRateValueRepo struct {
	prices map[string]float64 // sourceName → current price
	err    error
}

func (m *mockRateValueRepo) ObtainLastNRateValuesBySourceName(_ context.Context, name string, _ int64) ([]domain.RateValue, error) {
	if m.err != nil {
		return nil, m.err
	}
	price, ok := m.prices[name]
	if !ok {
		return nil, nil
	}
	return []domain.RateValue{{Price: price}}, nil
}

// mockRateSourceRepo is a test double for telegramRateSourceRepository.
type mockRateSourceRepo struct {
	sources map[string]domain.RateSource
	err     error
}

func (m *mockRateSourceRepo) ObtainRateSourcesByNames(_ context.Context, names []string) (map[string]domain.RateSource, error) {
	if m.err != nil {
		return nil, m.err
	}
	result := make(map[string]domain.RateSource, len(names))
	for _, n := range names {
		if src, ok := m.sources[n]; ok {
			result[n] = src
		}
	}
	return result, nil
}

// mockRateUserProfileRepo is a test double for rateUserProfileRepository.
type mockRateUserProfileRepo struct {
	profile *domain.RateUserProfile
	err     error
}

func (m *mockRateUserProfileRepo) ObtainRateUserProfileByUserID(_ context.Context, _ domain.UserType, _ string) (*domain.RateUserProfile, error) {
	return m.profile, m.err
}

const testChatID int64 = 123456789

// Compile-time interface checks.
var _ telegramClient = (*mockTelegramClient)(nil)
var _ subscriptionRepository = (*mockSubRepo)(nil)
var _ telegramRateValueRepository = (*mockRateValueRepo)(nil)
var _ telegramRateSourceRepository = (*mockRateSourceRepo)(nil)
var _ rateUserProfileRepository = (*mockRateUserProfileRepo)(nil)

// Compile-time check — ensures *integration.TelegramBotClient satisfies telegramClient.
var _ telegramClient = (*integration.TelegramBotClient)(nil)
