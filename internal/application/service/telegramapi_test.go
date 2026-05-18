package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/seilbekskindirov/monitor/internal/application/labelfmt"
	"github.com/seilbekskindirov/monitor/internal/domain"
	integration "github.com/seilbekskindirov/monitor/internal/infrastructure/telegrambot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramApi_handleShow(t *testing.T) {
	t.Parallel()
	t.Run("shows list when subscriptions exist", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subs := []domain.RateUserSubscription{
			{SourceName: "USD/KZT", ConditionType: domain.ConditionTypeDelta, ConditionValue: "0.5"},
			{SourceName: "EUR/KZT", ConditionType: domain.ConditionTypeInterval, ConditionValue: "30m"},
		}
		h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})

		h.handleShow(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "USD/KZT")
		assert.Contains(t, client.htmlMessages[0], "EUR/KZT")
		assert.Contains(t, client.htmlMessages[0], "Δ ≥ 0.5")
		assert.Contains(t, client.htmlMessages[0], "every 30m")
		require.Len(t, client.keyboards, 1)
	})
	t.Run("shows empty-state when no subscriptions", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{subs: []domain.RateUserSubscription{}}, &mockSourceRepo{})

		h.handleShow(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "no active subscriptions")
		require.Len(t, client.keyboards, 1)
	})
	t.Run("shows error message when repo fails", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{err: fmt.Errorf("db error")}, &mockSourceRepo{})

		h.handleShow(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "⚠️")
	})
}

func TestTelegramApi_handleShow_editMode(t *testing.T) {
	t.Parallel()
	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subs := []domain.RateUserSubscription{
			{SourceName: "USD/KZT", ConditionType: domain.ConditionTypeDelta, ConditionValue: "5"},
		}
		h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})

		h.handleShow(t.Context(), testChatID, 77)

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 77, client.editedMsgIDs[0])
		assert.Contains(t, client.editedTexts[0], "USD/KZT")
		assert.Empty(t, client.htmlMessages)
	})
	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subs := []domain.RateUserSubscription{
			{SourceName: "USD/KZT", ConditionType: domain.ConditionTypeDelta, ConditionValue: "5"},
		}
		h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})

		h.handleShow(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})
}

func TestTelegramApi_handleLatestUpdates(t *testing.T) {
	t.Parallel()
	t.Run("shows rate when LatestNotifiedRate is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subs := []domain.RateUserSubscription{
			{
				SourceName:         "USD/KZT",
				ConditionType:      domain.ConditionTypeDelta,
				ConditionValue:     "0.5",
				LatestNotifiedRate: 450.25,
				UpdatedAt:          time.Date(2026, 4, 12, 10, 30, 0, 0, time.UTC),
			},
		}
		h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "USD/KZT")
		assert.Contains(t, client.htmlMessages[0], "450.2500")
		assert.Contains(t, client.htmlMessages[0], "2026-04-12 10:30")
	})
	t.Run("shows no data yet when LatestNotifiedRate is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subs := []domain.RateUserSubscription{
			{SourceName: "USD/KZT", ConditionType: domain.ConditionTypeDelta, ConditionValue: "0.5"},
		}
		h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "no data yet")
	})
	t.Run("shows empty-state when user has no subscriptions", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{subs: []domain.RateUserSubscription{}}, &mockSourceRepo{})

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "no subscriptions yet")
	})
	t.Run("shows error message when repo fails", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{err: fmt.Errorf("db error")}, &mockSourceRepo{})

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "⚠️")
	})
}

func TestTelegramApi_handleLatestUpdates_editMode(t *testing.T) {
	t.Parallel()
	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subs := []domain.RateUserSubscription{
			{SourceName: "USD/KZT", ConditionType: domain.ConditionTypeDelta, ConditionValue: "5"},
		}
		h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})

		h.handleLatestUpdates(t.Context(), testChatID, 88)

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 88, client.editedMsgIDs[0])
		assert.Empty(t, client.htmlMessages)
	})
	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subs := []domain.RateUserSubscription{
			{SourceName: "USD/KZT", ConditionType: domain.ConditionTypeDelta, ConditionValue: "5"},
		}
		h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})

		h.handleLatestUpdates(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})
}

func TestTelegramApi_handleAddSourceList(t *testing.T) {
	t.Parallel()
	t.Run("shows source buttons when sources exist", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "usd_kzt", Title: "USD", BaseCurrency: "USD", QuoteCurrency: "KZT"},
			{Name: "eur_kzt", Title: "EUR", BaseCurrency: "EUR", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddSourceList(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		// 2 unique titles + 1 back row = 3 rows total.
		assert.Len(t, client.keyboards[0].InlineKeyboard, 3)
		for _, row := range client.keyboards[0].InlineKeyboard[:2] {
			require.NotNil(t, row[0].CallbackData)
			assert.True(t, strings.HasPrefix(*row[0].CallbackData, cbAddTitlePrefix))
		}
	})
	t.Run("deduplicates sources with the same title", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "qp_usd", Title: "QazPost", BaseCurrency: "USD", QuoteCurrency: "KZT"},
			{Name: "qp_rub", Title: "QazPost", BaseCurrency: "RUB", QuoteCurrency: "KZT"},
			{Name: "qp_eur", Title: "QazPost", BaseCurrency: "EUR", QuoteCurrency: "KZT"},
			{Name: "nbk", Title: "National Bank of Kazakhstan", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddSourceList(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		// 2 unique titles + 1 back row = 3 rows total
		assert.Len(t, client.keyboards[0].InlineKeyboard, 3)
	})
	t.Run("shows no-sources message when list is empty", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: []domain.RateSource{}})

		h.handleAddSourceList(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "No rate sources")
	})
	t.Run("shows error message when repo fails", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{err: fmt.Errorf("db error")})

		h.handleAddSourceList(t.Context(), testChatID, 0)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "⚠️")
	})
}

func TestTelegramApi_handleAddSourceList_editMode(t *testing.T) {
	t.Parallel()
	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "usd_kzt", Title: "USD", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddSourceList(t.Context(), testChatID, 55)

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 55, client.editedMsgIDs[0])
		assert.Empty(t, client.htmlMessages)
	})
	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "usd_kzt", Title: "USD", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddSourceList(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})
}

func TestTelegramApi_handleAddTitleSelect(t *testing.T) {
	t.Parallel()
	t.Run("shows currency pair buttons for matching sources", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "qp_usd", Title: "QazPost", BaseCurrency: "USD", QuoteCurrency: "KZT"},
			{Name: "qp_rub", Title: "QazPost", BaseCurrency: "RUB", QuoteCurrency: "KZT"},
			{Name: "nbk", Title: "National Bank", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddTitleSelect(t.Context(), testChatID, 0, "QazPost")

		require.Len(t, client.keyboards, 1)
		// 2 pair rows + 1 back row = 3 rows
		assert.Len(t, client.keyboards[0].InlineKeyboard, 3)
	})
	t.Run("pair button callback_data feeds into routeAddFlow", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "qp_usd", Title: "QazPost", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddTitleSelect(t.Context(), testChatID, 0, "QazPost")

		require.Len(t, client.keyboards, 1)
		pairRow := client.keyboards[0].InlineKeyboard[0]
		require.NotNil(t, pairRow[0].CallbackData)
		assert.True(t, strings.HasPrefix(*pairRow[0].CallbackData, cbAddSrcPrefix))
		assert.Contains(t, *pairRow[0].CallbackData, "qp_usd")
	})
	t.Run("back button callback_data equals cbAdd to return to title list", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "qp_usd", Title: "QazPost", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddTitleSelect(t.Context(), testChatID, 0, "QazPost")

		require.Len(t, client.keyboards, 1)
		kb := client.keyboards[0].InlineKeyboard
		backRow := kb[len(kb)-1]
		require.NotNil(t, backRow[0].CallbackData)
		assert.Equal(t, cbAdd, *backRow[0].CallbackData)
	})
	t.Run("shows no-pairs message when no source matches the title", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "nbk", Title: "National Bank", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddTitleSelect(t.Context(), testChatID, 0, "QazPost")

		require.Len(t, client.keyboards, 1)
		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "No currency pairs")
	})
	t.Run("shows error message when repo fails", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{err: fmt.Errorf("db error")})

		h.handleAddTitleSelect(t.Context(), testChatID, 0, "QazPost")

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "⚠️")
		assert.Empty(t, client.keyboards)
	})
}

func TestTelegramApi_handleAddTitleSelect_editMode(t *testing.T) {
	t.Parallel()
	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "qp_usd", Title: "QazPost", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddTitleSelect(t.Context(), testChatID, 44, "QazPost")

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 44, client.editedMsgIDs[0])
		assert.Empty(t, client.htmlMessages)
	})
	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		sources := []domain.RateSource{
			{Name: "qp_usd", Title: "QazPost", BaseCurrency: "USD", QuoteCurrency: "KZT"},
		}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{sources: sources})

		h.handleAddTitleSelect(t.Context(), testChatID, 0, "QazPost")

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})
}

// Task 3: TestTelegramApi_handleAddSourceSelect replaces the removed stateful handleAddCondition test.
func TestTelegramApi_handleAddSourceSelect(t *testing.T) {
	t.Parallel()
	t.Run("sends condition-type keyboard containing the source name", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleAddSourceSelect(t.Context(), testChatID, 0, "usd_kzt")

		require.Len(t, client.keyboards, 1)
		assert.Len(t, client.keyboards[0].InlineKeyboard, 5) // 4 condition rows + back
		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "usd_kzt")
	})
	t.Run("embeds url-encoded source name in each condition button callback_data", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleAddSourceSelect(t.Context(), testChatID, 0, "Halyk Bank")

		require.Len(t, client.keyboards, 1)
		for _, row := range client.keyboards[0].InlineKeyboard[:4] {
			require.NotNil(t, row[0].CallbackData)
			assert.Contains(t, *row[0].CallbackData, "Halyk")
		}
	})
}

func TestTelegramApi_handleAddSourceSelect_editMode(t *testing.T) {
	t.Parallel()
	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleAddSourceSelect(t.Context(), testChatID, 33, "usd_kzt")

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 33, client.editedMsgIDs[0])
		assert.Empty(t, client.htmlMessages)
	})
	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleAddSourceSelect(t.Context(), testChatID, 0, "usd_kzt")

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})
}

// Task 5: TestTelegramApi_handleAddValueSelect replaces the removed stateful handleAddText test.
func TestTelegramApi_handleAddValueSelect(t *testing.T) {
	t.Parallel()
	t.Run("delta condition renders 5 value buttons plus back row", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleAddValueSelect(t.Context(), testChatID, 0, "usd_kzt", domain.ConditionTypeDelta)

		require.Len(t, client.keyboards, 1)
		assert.Len(t, client.keyboards[0].InlineKeyboard, 6) // 5 values + back
	})
	t.Run("interval condition renders 6 value buttons plus back row", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleAddValueSelect(t.Context(), testChatID, 0, "usd_kzt", domain.ConditionTypeInterval)

		require.Len(t, client.keyboards, 1)
		assert.Len(t, client.keyboards[0].InlineKeyboard, 7) // 6 values + back
	})
	t.Run("daily condition renders 8 value buttons plus back row", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleAddValueSelect(t.Context(), testChatID, 0, "usd_kzt", domain.ConditionTypeDaily)

		require.Len(t, client.keyboards, 1)
		assert.Len(t, client.keyboards[0].InlineKeyboard, 9) // 8 values + back
	})
	t.Run("cron condition renders 7 value buttons plus back row", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleAddValueSelect(t.Context(), testChatID, 0, "usd_kzt", domain.ConditionTypeCron)

		require.Len(t, client.keyboards, 1)
		assert.Len(t, client.keyboards[0].InlineKeyboard, 8) // 7 values + back
	})
	t.Run("unknown condition type sends warning and no keyboard", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleAddValueSelect(t.Context(), testChatID, 0, "usd_kzt", "unknown")

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], "⚠️")
		assert.Empty(t, client.keyboards)
	})
}

func TestTelegramApi_routeAddFlow(t *testing.T) {
	t.Parallel()
	t.Run("saves subscription when rest has 3 segments", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subRepo := &mockSubRepo{}
		h := newTelegramApi(client, subRepo, &mockSourceRepo{})

		// rest = "<urlencoded_source>:<conditionType>:<value>"
		h.routeAddFlow(t.Context(), testChatID, 0, "usd_kzt:delta:0.5")

		require.True(t, subRepo.retained)
		assert.Equal(t, "usd_kzt", subRepo.lastRetained.SourceName)
		assert.Equal(t, domain.ConditionTypeDelta, subRepo.lastRetained.ConditionType)
		assert.Equal(t, "0.5", subRepo.lastRetained.ConditionValue)
	})
	t.Run("shows condition keyboard when rest has 1 segment", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.routeAddFlow(t.Context(), testChatID, 0, "usd_kzt")

		require.Len(t, client.keyboards, 1)
		assert.Len(t, client.keyboards[0].InlineKeyboard, 5) // 4 conditions + back
	})
	t.Run("shows value keyboard when rest has 2 segments", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.routeAddFlow(t.Context(), testChatID, 0, "usd_kzt:delta")

		require.Len(t, client.keyboards, 1)
		assert.Len(t, client.keyboards[0].InlineKeyboard, 6) // 5 delta values + back
	})
}

func TestTelegramApi_handleDeleteList_editMode(t *testing.T) {
	t.Parallel()
	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subs := []domain.RateUserSubscription{
			{SourceName: "usd_kzt", ConditionType: domain.ConditionTypeDelta, ConditionValue: "5"},
		}
		h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})

		h.handleDeleteList(t.Context(), testChatID, 11)

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 11, client.editedMsgIDs[0])
		assert.Empty(t, client.htmlMessages)
	})
	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subs := []domain.RateUserSubscription{
			{SourceName: "usd_kzt", ConditionType: domain.ConditionTypeDelta, ConditionValue: "5"},
		}
		h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})

		h.handleDeleteList(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})
}

func TestTelegramApi_handleDeleteAsk_editMode(t *testing.T) {
	t.Parallel()
	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleDeleteAsk(t.Context(), testChatID, 22, "usd_kzt")

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 22, client.editedMsgIDs[0])
		assert.Empty(t, client.htmlMessages)
	})
	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.handleDeleteAsk(t.Context(), testChatID, 0, "usd_kzt")

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})
}

func TestTelegramApi_handleDeleteConfirm(t *testing.T) {
	t.Parallel()
	// Regression: delete must pass the subscription's real ID to RemoveRateUserSubscription,
	// not an empty string. Previously the delete was a silent no-op because ID was not set.
	t.Run("passes correct ID to RemoveRateUserSubscription", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subRepo := &mockSubRepo{
			subs: []domain.RateUserSubscription{
				{
					ID:         "RUS-test-id-001",
					UserType:   domain.UserTypeTelegram,
					UserID:     strconv.FormatInt(testChatID, 10),
					SourceName: "usd_kzt",
				},
			},
		}
		h := newTelegramApi(client, subRepo, &mockSourceRepo{})

		h.handleDeleteConfirm(t.Context(), testChatID, 0, "usd_kzt")

		require.True(t, subRepo.removed)
		assert.Equal(t, "RUS-test-id-001", subRepo.lastRemoved.ID)
	})
	t.Run("deletes subscription and shows main menu on success", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		subRepo := &mockSubRepo{
			subs: []domain.RateUserSubscription{
				{
					ID:         "sub-abc-123",
					UserType:   domain.UserTypeTelegram,
					UserID:     strconv.FormatInt(testChatID, 10),
					SourceName: "usd_kzt",
				},
			},
		}
		h := newTelegramApi(client, subRepo, &mockSourceRepo{})

		h.handleDeleteConfirm(t.Context(), testChatID, 0, "usd_kzt")

		// First message: deletion confirmation. Second message: main menu (with keyboard).
		require.Len(t, client.htmlMessages, 2)
		assert.Contains(t, client.htmlMessages[0], "deleted")
		assert.Contains(t, client.htmlMessages[1], "Subscription Management")
		require.Len(t, client.keyboards, 1, "main menu keyboard must be sent")
	})
	t.Run("sends not-found message when subscription is missing", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{subs: []domain.RateUserSubscription{}}, &mockSourceRepo{})

		h.handleDeleteConfirm(t.Context(), testChatID, 0, "usd_kzt")

		require.Len(t, client.htmlMessages, 2)
		assert.Contains(t, client.htmlMessages[0], "not found")
		assert.Contains(t, client.htmlMessages[1], "Subscription Management")
	})
	t.Run("sends error message and shows main menu on repo failure", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{err: fmt.Errorf("db error")}, &mockSourceRepo{})

		h.handleDeleteConfirm(t.Context(), testChatID, 0, "usd_kzt")

		// First message: error text. Second message: main menu (with keyboard).
		require.Len(t, client.htmlMessages, 2)
		assert.Contains(t, client.htmlMessages[0], "⚠️")
		assert.Contains(t, client.htmlMessages[1], "Subscription Management")
		require.Len(t, client.keyboards, 1, "main menu keyboard must be sent after error")
	})
}

// Task 6: TestTelegramApi_handleMessage strips all state-machine subtests — the handler is now
// stateless. Only command routing and unrecognised-input hint are tested.
func TestTelegramApi_handleMessage(t *testing.T) {
	t.Parallel()
	t.Run("sends main menu on /subscriptions command", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})
		msg := buildMessage(testChatID, "/subscriptions")

		h.handleMessage(t.Context(), msg)

		require.Len(t, client.keyboards, 1, "main menu keyboard must be sent")
	})
	t.Run("sends main menu on /start command", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})
		msg := buildMessage(testChatID, "/start")

		h.handleMessage(t.Context(), msg)

		require.Len(t, client.keyboards, 1)
	})
	t.Run("sends main menu on case-insensitive and padded command", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})
		msg := buildMessage(testChatID, "  /SUBSCRIPTIONS  ")

		h.handleMessage(t.Context(), msg)

		require.Len(t, client.keyboards, 1)
	})
	t.Run("sends hint containing /subscriptions on unrecognised input", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})
		msg := buildMessage(testChatID, "hello")

		h.handleMessage(t.Context(), msg)

		require.Len(t, client.htmlMessages, 1)
		assert.Contains(t, client.htmlMessages[0], commandSubscriptions)
		assert.Empty(t, client.keyboards)
	})
}

func TestTelegramApi_sendMainMenu(t *testing.T) {
	t.Parallel()
	t.Run("edits existing message when msgID is non-zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.sendMainMenu(t.Context(), testChatID, 42)

		require.Len(t, client.editedMsgIDs, 1)
		assert.Equal(t, 42, client.editedMsgIDs[0])
		assert.Empty(t, client.htmlMessages, "must not send a new message")
	})
	t.Run("sends new message when msgID is zero", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.sendMainMenu(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		assert.Empty(t, client.editedMsgIDs)
	})
	t.Run("keyboard contains Mini App WebApp button when webAppURL is set", func(t *testing.T) {
		t.Parallel()
		const wantURL = "https://example.com/app/subscriptions"
		client := &mockTelegramClient{}
		h := newTelegramApiWithWebApp(client, &mockSubRepo{}, &mockSourceRepo{}, wantURL)

		h.sendMainMenu(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		kb := client.keyboards[0].InlineKeyboard
		// 3 standard rows + 1 Mini App WebApp row = 4 rows total.
		require.Len(t, kb, 4)
		webAppRow := kb[3]
		require.Len(t, webAppRow, 1)
		require.NotNil(t, webAppRow[0].WebApp, "last row must carry a WebApp button, not a URL button")
		assert.Equal(t, wantURL, webAppRow[0].WebApp.URL)
		assert.Empty(t, webAppRow[0].URL, "WebApp button must not set the URL field")
	})
	t.Run("keyboard has no Mini App button when webAppURL is empty", func(t *testing.T) {
		t.Parallel()
		client := &mockTelegramClient{}
		h := newTelegramApi(client, &mockSubRepo{}, &mockSourceRepo{})

		h.sendMainMenu(t.Context(), testChatID, 0)

		require.Len(t, client.keyboards, 1)
		kb := client.keyboards[0].InlineKeyboard
		// Standard 3 rows only.
		require.Len(t, kb, 3)
	})
}

// Task 7: TestSubscriptionConditionLabel — fixed expected strings and added interval alias cases.
func TestSubscriptionConditionLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		sub      domain.RateUserSubscription
		expected string
	}{
		{
			name:     "delta appends percent sign",
			sub:      domain.RateUserSubscription{ConditionType: domain.ConditionTypeDelta, ConditionValue: "0.5"},
			expected: "Δ ≥ 0.5%",
		},
		{
			name:     "interval passthrough for short duration",
			sub:      domain.RateUserSubscription{ConditionType: domain.ConditionTypeInterval, ConditionValue: "30m"},
			expected: "every 30m",
		},
		{
			name:     "interval_24h maps to 1d",
			sub:      domain.RateUserSubscription{ConditionType: domain.ConditionTypeInterval, ConditionValue: "24h"},
			expected: "every 1d",
		},
		{
			name:     "interval_168h maps to 1w",
			sub:      domain.RateUserSubscription{ConditionType: domain.ConditionTypeInterval, ConditionValue: "168h"},
			expected: "every 1w",
		},
		{
			name:     "daily truncates to HH:MM UTC",
			sub:      domain.RateUserSubscription{ConditionType: domain.ConditionTypeDaily, ConditionValue: "08:00:00"},
			expected: "daily at 08:00 UTC",
		},
		{
			name:     "cron renders weekday name",
			sub:      domain.RateUserSubscription{ConditionType: domain.ConditionTypeCron, ConditionValue: "0 9 * * 1"},
			expected: "weekly on Monday (UTC 09:00)",
		},
		{
			name:     "unknown condition falls back to raw type string",
			sub:      domain.RateUserSubscription{ConditionType: "unknown", ConditionValue: ""},
			expected: "unknown",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, labelfmt.SubscriptionConditionLabel(tc.sub))
		})
	}
}

// Task 8: TestConditionFromString — added daily and cron branches.
func TestConditionFromString(t *testing.T) {
	t.Parallel()
	t.Run("returns delta for delta string", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, domain.ConditionTypeDelta, conditionFromString("delta"))
	})
	t.Run("returns interval for interval string", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, domain.ConditionTypeInterval, conditionFromString("interval"))
	})
	t.Run("returns daily for daily string", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, domain.ConditionTypeDaily, conditionFromString("daily"))
	})
	t.Run("returns cron for cron string", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, domain.ConditionTypeCron, conditionFromString("cron"))
	})
	t.Run("returns empty string for unknown input", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, domain.SubscriptionConditionType(""), conditionFromString("unknown"))
	})
}

func BenchmarkHandleShow(b *testing.B) {
	subs := make([]domain.RateUserSubscription, 100)
	for i := range subs {
		subs[i] = domain.RateUserSubscription{
			SourceName:     fmt.Sprintf("source_%d", i),
			ConditionType:  domain.ConditionTypeDelta,
			ConditionValue: "0.5",
		}
	}
	client := &mockTelegramClient{}
	h := newTelegramApi(client, &mockSubRepo{subs: subs}, &mockSourceRepo{})
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		client.reset()
		h.handleShow(ctx, testChatID, 0)
	}
}

// newTelegramApi is a helper that creates a TelegramApi wired to the provided mocks.
// Regen is disabled (nil generator, nil locks, 0 adminChatID, nil factory).
func newTelegramApi(client *mockTelegramClient, subRepo subscriptionRepository, sourceRepo sourceRepository) *TelegramApi {
	h, _ := NewTelegramApi(client, subRepo, sourceRepo, "", nil, nil, 0, nil)
	return h
}

// newTelegramApiWithWebApp creates a TelegramApi with a non-empty webAppURL for WebApp button tests.
func newTelegramApiWithWebApp(client *mockTelegramClient, subRepo subscriptionRepository, sourceRepo sourceRepository, webAppURL string) *TelegramApi {
	h, _ := NewTelegramApi(client, subRepo, sourceRepo, webAppURL, nil, nil, 0, nil)
	return h
}

// buildMessage constructs a minimal *tgbotapi.Message for testing handleMessage.
// Note: the OvyFlash fork changed Message.Chat from *Chat to Chat (value receiver).
func buildMessage(chatID int64, text string) *tgbotapi.Message {
	return &tgbotapi.Message{
		Chat: tgbotapi.Chat{ID: chatID},
		Text: text,
	}
}

// mockTelegramClient records all outbound messages and keyboards for assertion.
// Note: keyboards slice is shared between SendHTMLMessageWithKeyboard and
// EditHTMLMessageWithKeyboard calls. Use editedMsgIDs length to discriminate.
type mockTelegramClient struct {
	mu           sync.Mutex
	htmlMessages []string
	keyboards    []tgbotapi.InlineKeyboardMarkup
	answeredCBs  []string
	editedMsgIDs []int    // tracks messageID of each EditHTMLMessageWithKeyboard call
	editedTexts  []string // tracks text of each EditHTMLMessageWithKeyboard call
}

func (m *mockTelegramClient) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.htmlMessages = m.htmlMessages[:0]
	m.keyboards = m.keyboards[:0]
	m.answeredCBs = m.answeredCBs[:0]
	m.editedMsgIDs = m.editedMsgIDs[:0]
	m.editedTexts = m.editedTexts[:0]
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
	subs         []domain.RateUserSubscription
	err          error
	retained     bool
	lastRetained domain.RateUserSubscription
	removed      bool
	lastRemoved  domain.RateUserSubscription
}

func (m *mockSubRepo) ObtainRateUserSubscriptionsByUserID(_ context.Context, _ domain.UserType, _ string) ([]domain.RateUserSubscription, error) {
	return m.subs, m.err
}

func (m *mockSubRepo) RetainRateUserSubscription(_ context.Context, sub *domain.RateUserSubscription) error {
	if m.err != nil {
		return m.err
	}
	m.retained = true
	m.lastRetained = *sub
	return nil
}

func (m *mockSubRepo) RemoveRateUserSubscription(_ context.Context, sub *domain.RateUserSubscription) error {
	if m.err != nil {
		return m.err
	}
	m.removed = true
	m.lastRemoved = *sub
	return nil
}

const testChatID int64 = 123456789

// mockSourceRepo is a test double for sourceRepository.
type mockSourceRepo struct {
	sources []domain.RateSource
	byName  map[string]*domain.RateSource
	err     error
}

func (m *mockSourceRepo) ObtainRateSourceByName(_ context.Context, name string) (*domain.RateSource, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.byName != nil {
		return m.byName[name], nil
	}
	return nil, nil
}

func (m *mockSourceRepo) ObtainAllRateSources(_ context.Context) ([]domain.RateSource, error) {
	return m.sources, m.err
}

// Compile-time interface check — ensures mockTelegramClient satisfies telegramClient.
var _ telegramClient = (*mockTelegramClient)(nil)

// Compile-time interface check — ensures *integration.TelegramBotClient satisfies telegramClient.
var _ telegramClient = (*integration.TelegramBotClient)(nil)
