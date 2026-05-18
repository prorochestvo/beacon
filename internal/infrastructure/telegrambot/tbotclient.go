// Package telegrambot wraps the OvyFlash telegram-bot-api library and provides
// a higher-level client used by the application's notification and bot-command layers.
package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/monitor/internal"
)

// UpdateHandler is called for every incoming Telegram update in the event bus.
type UpdateHandler func(ctx context.Context, update tgbotapi.Update)

// TelegramChatID is a typed int64 that identifies a Telegram chat or user.
type TelegramChatID int64

// NewTBotClient parses the TELEGRAMBOT_DSN, validates the bot token and admin
// chat ID, connects to the Telegram Bot API, and returns a ready-to-use client.
// The DSN format is <adminChatID>:<botToken>@<host>.
func NewTBotClient(tbotDSN dsninjector.DataSource, logger io.Writer) (*TelegramBotClient, error) {
	rx := regexp.MustCompile(regexpTelegramToken)

	token := strings.TrimSpace(tbotDSN.Addr())
	if token == "" || rx.MatchString(token) == false {
		return nil, errors.New("telegram: bot token is required")
	}

	adminChatID, err := strconv.ParseInt(tbotDSN.Login(), 10, 64)
	if err != nil || adminChatID == 0 {
		if err == nil {
			err = fmt.Errorf("admin chat id cannot be zero")
		}
		err = fmt.Errorf("invalid admin chat id: %w", err)
		return nil, errors.Join(err, internal.NewTraceError())
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("telegram: init bot: %w", err)
	}

	bot.Debug = false

	t := &TelegramBotClient{
		bot:         bot,
		adminChatID: TelegramChatID(adminChatID),
		logger:      logger,
	}

	return t, nil
}

// TelegramBotClient is a high-level Telegram bot client that wraps tgbotapi.BotAPI
// with typed send helpers, an update listener, and admin-targeted convenience methods.
type TelegramBotClient struct {
	bot         *tgbotapi.BotAPI
	adminChatID TelegramChatID
	logger      io.Writer
}

// BotToken returns the bot token used to validate Telegram WebApp initData.
// Do not log the returned value — it is a secret.
func (tbot *TelegramBotClient) BotToken() string { return tbot.bot.Token }

// AdminChatID returns the Telegram chat id of the configured admin.
// Used by HTTP handlers that need to gate admin-only operations.
func (tbot *TelegramBotClient) AdminChatID() int64 { return int64(tbot.adminChatID) }

// Ping verifies the Telegram Bot API is reachable by calling GetMe and checking
// that the returned bot ID is non-zero.
func (tbot *TelegramBotClient) Ping(_ context.Context) error {
	u, err := tbot.bot.GetMe()
	if err != nil {
		return errors.Join(err, internal.NewTraceError())
	}
	if u.ID == 0 {
		return errors.New("telegram: bot ping failed: invalid response")
	}
	return nil
}

// Me returns the bot's own chat ID and username by calling GetMe.
func (tbot *TelegramBotClient) Me(_ context.Context) (TelegramChatID, string, error) {
	u, err := tbot.bot.GetMe()
	if err != nil {
		return 0, "", errors.Join(err, internal.NewTraceError())
	}
	if u.ID == 0 {
		return 0, "", errors.New("telegram: bot ping failed: invalid response")
	}
	return TelegramChatID(u.ID), u.UserName, nil
}

// SendPlainTextMessageToAdmin sends a plain-text message to the configured admin chat.
func (tbot *TelegramBotClient) SendPlainTextMessageToAdmin(ctx context.Context, text string) error {
	return tbot.SendPlainTextMessage(ctx, tbot.adminChatID, text)
}

// SendPlainTextMessage sends a plain-text (no parse mode) message to chatID.
func (tbot *TelegramBotClient) SendPlainTextMessage(ctx context.Context, chatID TelegramChatID, text string) error {
	m := tgbotapi.NewMessage(int64(chatID), text)
	return tbot.emit(ctx, &m)
}

// SendMarkdownMessageToAdmin sends a Markdown-formatted message to the configured admin chat.
func (tbot *TelegramBotClient) SendMarkdownMessageToAdmin(ctx context.Context, text string) error {
	return tbot.SendMarkdownMessage(ctx, tbot.adminChatID, text)
}

// SendMarkdownMessage sends a Markdown-formatted message to chatID.
func (tbot *TelegramBotClient) SendMarkdownMessage(ctx context.Context, chatID TelegramChatID, text string) error {
	m := tgbotapi.NewMessage(int64(chatID), text)
	m.ParseMode = tgbotapi.ModeMarkdown
	return tbot.emit(ctx, &m)
}

// SendHTMLMessageToAdmin sends an HTML-formatted message to the configured admin chat.
func (tbot *TelegramBotClient) SendHTMLMessageToAdmin(ctx context.Context, text string) error {
	return tbot.SendHTMLMessage(ctx, tbot.adminChatID, text)
}

// SendHTMLMessage sends an HTML-formatted message to chatID.
func (tbot *TelegramBotClient) SendHTMLMessage(ctx context.Context, chatID TelegramChatID, text string) error {
	m := tgbotapi.NewMessage(int64(chatID), text)
	m.ParseMode = tgbotapi.ModeHTML
	return tbot.emit(ctx, &m)
}

// SendHTMLMessageReturning sends an HTML-formatted message and returns the
// message id assigned by Telegram. Use this when you need to edit the message
// in place later (send-then-edit pattern). The existing SendHTMLMessage is kept
// unchanged so callers that do not need the id are unaffected.
func (tbot *TelegramBotClient) SendHTMLMessageReturning(_ context.Context, chatID TelegramChatID, text string) (int, error) {
	m := tgbotapi.NewMessage(int64(chatID), text)
	m.ParseMode = tgbotapi.ModeHTML
	msg, err := tbot.bot.Send(m)
	if err != nil {
		return 0, errors.Join(err, internal.NewTraceError())
	}
	return msg.MessageID, nil
}

// SendDocumentToAdmin uploads content as a named file to the configured admin chat.
func (tbot *TelegramBotClient) SendDocumentToAdmin(ctx context.Context, fileName string, fileContent []byte) error {
	return tbot.SendDocument(ctx, tbot.adminChatID, fileName, fileContent)
}

// SendDocument uploads content as a file named name to chatID.
func (tbot *TelegramBotClient) SendDocument(ctx context.Context, chatID TelegramChatID, fileName string, fileContent []byte) error {
	d := tgbotapi.NewDocument(int64(chatID), tgbotapi.FileBytes{Name: fileName, Bytes: fileContent})
	return tbot.emit(ctx, d)
}

// emit dispatches a Chattable message via the bot API and discards the returned message.
func (tbot *TelegramBotClient) emit(_ context.Context, m tgbotapi.Chattable) error {
	if _, err := tbot.bot.Send(m); err != nil {
		return errors.Join(err, internal.NewTraceError())
	}
	return nil
}

// SendHTMLMessageWithKeyboard sends an HTML-formatted message with an inline keyboard to chatID.
func (tbot *TelegramBotClient) SendHTMLMessageWithKeyboard(_ context.Context, chatID TelegramChatID, text string, keyboard tgbotapi.InlineKeyboardMarkup) error {
	m := tgbotapi.NewMessage(int64(chatID), text)
	m.ParseMode = tgbotapi.ModeHTML
	m.ReplyMarkup = keyboard
	if _, err := tbot.bot.Send(m); err != nil {
		return errors.Join(err, internal.NewTraceError())
	}
	return nil
}

// AnswerCallbackQuery acknowledges a button press, clearing the loading spinner.
func (tbot *TelegramBotClient) AnswerCallbackQuery(_ context.Context, callbackQueryID, text string) error {
	cfg := tgbotapi.NewCallback(callbackQueryID, text)
	if _, err := tbot.bot.Request(cfg); err != nil {
		return errors.Join(err, internal.NewTraceError())
	}
	return nil
}

// EditMessageText replaces the text of an existing message in place.
// "message is not modified" errors from Telegram are silently ignored.
func (tbot *TelegramBotClient) EditMessageText(_ context.Context, chatID TelegramChatID, messageID int, text string) error {
	edit := tgbotapi.NewEditMessageText(int64(chatID), messageID, text)
	edit.ParseMode = tgbotapi.ModeHTML
	if _, err := tbot.bot.Send(edit); err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return nil
		}
		return errors.Join(err, internal.NewTraceError())
	}
	return nil
}

// EditHTMLMessageWithKeyboard replaces the text and inline keyboard of an existing message.
// "message is not modified" and "message to edit not found" errors are silently ignored
// so callers do not need to handle them.
func (tbot *TelegramBotClient) EditHTMLMessageWithKeyboard(
	_ context.Context,
	chatID TelegramChatID,
	messageID int,
	text string,
	keyboard tgbotapi.InlineKeyboardMarkup,
) error {
	edit := tgbotapi.NewEditMessageText(int64(chatID), messageID, text)
	edit.ParseMode = tgbotapi.ModeHTML
	edit.ReplyMarkup = &keyboard
	if _, err := tbot.bot.Send(edit); err != nil {
		s := err.Error()
		if strings.Contains(s, "message is not modified") ||
			strings.Contains(s, "message to edit not found") {
			return nil
		}
		return errors.Join(err, internal.NewTraceError())
	}
	return nil
}

// Listen starts long-polling and dispatches every incoming update to handler.
// Blocks until ctx is cancelled — run it in a goroutine.
func (tbot *TelegramBotClient) Listen(ctx context.Context, handler UpdateHandler) {
	log.Println("telegram: bot started listening for updates")
	defer log.Println("telegram: bot stopped listening for updates")

	isTerminated := false

	cfg := tgbotapi.NewUpdate(0)
	cfg.Timeout = 30
	updates := tbot.bot.GetUpdatesChan(cfg)

	for !isTerminated {
		select {
		case <-ctx.Done():
			tbot.bot.StopReceivingUpdates()
			isTerminated = true
		case update, ok := <-updates:
			if !ok {
				return
			}
			handler(ctx, update)
		}
	}
}

const regexpTelegramToken = `^\d{9,}:[a-zA-Z0-9_-]{35,}$`
