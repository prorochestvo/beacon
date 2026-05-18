package telegrambot

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
