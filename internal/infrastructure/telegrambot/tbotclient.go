// Package telegrambot wraps the OvyFlash telegram-bot-api library and provides
// a higher-level client used by the application's notification and bot-command layers.
package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/prorochestvo/dsninjector"
	"github.com/prorochestvo/loginjector"
)

// updatePollTimeoutSeconds is the long-poll hold time asked of the Bot API. Thirty
// seconds is Telegram's own recommendation: long enough that an idle bot makes two
// requests a minute rather than a flood, short enough that a dropped connection is
// noticed within one cycle.
const updatePollTimeoutSeconds = 30

// TelegramChatID is a typed int64 that identifies a Telegram chat or user.
type TelegramChatID int64

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
		return errors.Join(err, loginjector.NewTraceError())
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
		return 0, "", errors.Join(err, loginjector.NewTraceError())
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
	return tbot.emit(ctx, &m, int64(chatID), "text")
}

// SendMarkdownMessageToAdmin sends a Markdown-formatted message to the configured admin chat.
func (tbot *TelegramBotClient) SendMarkdownMessageToAdmin(ctx context.Context, text string) error {
	return tbot.SendMarkdownMessage(ctx, tbot.adminChatID, text)
}

// SendMarkdownMessage sends a Markdown-formatted message to chatID.
func (tbot *TelegramBotClient) SendMarkdownMessage(ctx context.Context, chatID TelegramChatID, text string) error {
	m := tgbotapi.NewMessage(int64(chatID), text)
	m.ParseMode = tgbotapi.ModeMarkdown
	return tbot.emit(ctx, &m, int64(chatID), "markdown")
}

// SendHTMLMessageToAdmin sends an HTML-formatted message to the configured admin chat.
func (tbot *TelegramBotClient) SendHTMLMessageToAdmin(ctx context.Context, text string) error {
	return tbot.SendHTMLMessage(ctx, tbot.adminChatID, text)
}

// SendHTMLMessage sends an HTML-formatted message to chatID.
func (tbot *TelegramBotClient) SendHTMLMessage(ctx context.Context, chatID TelegramChatID, text string) error {
	m := tgbotapi.NewMessage(int64(chatID), text)
	m.ParseMode = tgbotapi.ModeHTML
	return tbot.emit(ctx, &m, int64(chatID), "html")
}

// SendHTMLMessageReturning sends an HTML-formatted message and returns the
// Telegram-assigned message id. Use it when you need to edit the message in
// place later (send-then-edit pattern).
func (tbot *TelegramBotClient) SendHTMLMessageReturning(_ context.Context, chatID TelegramChatID, text string) (int, error) {
	m := tgbotapi.NewMessage(int64(chatID), text)
	m.ParseMode = tgbotapi.ModeHTML
	msg, err := tbot.dispatch(m, int64(chatID), "html")
	if err != nil {
		return 0, errors.Join(err, loginjector.NewTraceError())
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
	return tbot.emit(ctx, d, int64(chatID), "document")
}

// SendHTMLMessageWithKeyboard sends an HTML-formatted message with an inline keyboard to chatID.
func (tbot *TelegramBotClient) SendHTMLMessageWithKeyboard(_ context.Context, chatID TelegramChatID, text string, keyboard tgbotapi.InlineKeyboardMarkup) error {
	m := tgbotapi.NewMessage(int64(chatID), text)
	m.ParseMode = tgbotapi.ModeHTML
	m.ReplyMarkup = keyboard
	if _, err := tbot.dispatch(m, int64(chatID), "html+kb"); err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

// AnswerCallbackQuery acknowledges a button press, clearing the loading spinner.
func (tbot *TelegramBotClient) AnswerCallbackQuery(_ context.Context, callbackQueryID, text string) error {
	cfg := tgbotapi.NewCallback(callbackQueryID, text)
	if _, err := tbot.bot.Request(cfg); err != nil {
		log.Printf("telegram: send kind=callback_ack err=%v", err)
		return errors.Join(err, loginjector.NewTraceError())
	}
	log.Printf("telegram: send kind=callback_ack")
	return nil
}

// EditMessageText replaces the text of an existing message in place.
// "message is not modified" errors from Telegram are silently ignored.
func (tbot *TelegramBotClient) EditMessageText(_ context.Context, chatID TelegramChatID, messageID int, text string) error {
	edit := tgbotapi.NewEditMessageText(int64(chatID), messageID, text)
	edit.ParseMode = tgbotapi.ModeHTML
	if _, err := tbot.dispatch(edit, int64(chatID), "edit"); err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return nil
		}
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

// EditHTMLMessageWithKeyboard replaces the text and inline keyboard of an existing message.
// "message is not modified" and "message to edit not found" errors are silently ignored.
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
	if _, err := tbot.dispatch(edit, int64(chatID), "edit+kb"); err != nil {
		s := err.Error()
		if strings.Contains(s, "message is not modified") ||
			strings.Contains(s, "message to edit not found") {
			return nil
		}
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

// Updates starts long-polling and returns a channel carrying every incoming
// update. It returns immediately; the polling runs until ctx is cancelled or the
// Bot API closes its own channel, and the returned channel is closed on the way
// out so a consumer ranging over it ends with the context rather than having to
// watch both.
//
// This hands out values rather than taking a callback because fetching updates is
// protocol and deciding what one means is not: the dispatch loop belongs to the
// gateway, and moving to webhooks should replace this method without touching a
// handler.
func (tbot *TelegramBotClient) Updates(ctx context.Context) <-chan tgbotapi.Update {
	out := make(chan tgbotapi.Update)

	// The client is handed a logger and, until now, never used it: these two lines
	// went to the global one instead. Routing them through the field is what lets a
	// caller — and the test below — see the loop start and stop.
	logger := tbot.logger
	if logger == nil {
		logger = io.Discard
	}

	go func() {
		defer close(out)

		fmt.Fprintln(logger, "telegram: bot started listening for updates")
		defer fmt.Fprintln(logger, "telegram: bot stopped listening for updates")

		cfg := tgbotapi.NewUpdate(0)
		cfg.Timeout = updatePollTimeoutSeconds
		updates := tbot.bot.GetUpdatesChan(cfg)

		for {
			select {
			case <-ctx.Done():
				tbot.bot.StopReceivingUpdates()
				return
			case update, ok := <-updates:
				if !ok {
					return
				}
				// The send is guarded by ctx as well: a consumer that has stopped
				// reading would otherwise pin this goroutine — and the poller behind
				// it — past cancellation, which is the shutdown hang this loop exists
				// to avoid.
				select {
				case out <- update:
				case <-ctx.Done():
					tbot.bot.StopReceivingUpdates()
					return
				}
			}
		}
	}()

	return out
}

// emit dispatches a Chattable message via the bot API, writes an access-log
// line, and discards the returned message. chatID and kind are passed by the
// caller so the log line carries stable, typed metadata without introspecting
// tgbotapi.Chattable.
func (tbot *TelegramBotClient) emit(_ context.Context, m tgbotapi.Chattable, chatID int64, kind string) error {
	if _, err := tbot.dispatch(m, chatID, kind); err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}
	return nil
}

// dispatch is the single fan-in for tbot.bot.Send + access-log. Returns the
// Telegram-assigned message so callers (e.g. SendHTMLMessageReturning) can
// recover the message id. The log line mirrors the inbound update format
// emitted by service.TelegramApi.Handle: "telegram: send chat=X kind=Y ...".
func (tbot *TelegramBotClient) dispatch(m tgbotapi.Chattable, chatID int64, kind string) (tgbotapi.Message, error) {
	msg, err := tbot.bot.Send(m)
	if err != nil {
		log.Printf("telegram: send chat=%d kind=%s err=%v", chatID, kind, err)
		return msg, err
	}
	log.Printf("telegram: send chat=%d kind=%s msg_id=%d", chatID, kind, msg.MessageID)
	return msg, nil
}

const regexpTelegramToken = `^\d{9,}:[a-zA-Z0-9_-]{35,}$`

// NewTBotClient parses the BEACON_TELEGRAMBOT_DSN, validates the bot token and admin
// chat ID, connects to the Telegram Bot API, and returns a ready-to-use client.
// The DSN format is <adminChatID>:<botToken>@<host>.
//
// The HTTP client hardcoded into the bot uses an explicit empty proxy transport
// so Telegram traffic never routes via the process-wide proxy. Do not change
// this without coordinating with the proxy wiring policy.
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
		return nil, errors.Join(err, loginjector.NewTraceError())
	}

	// Transport whose Proxy always returns nil: Telegram Bot API traffic is
	// always direct and never flows through any process-wide proxy, even if
	// HTTPS_PROXY or HTTP_PROXY is set in the environment.
	noProxyTransport := &http.Transport{
		//nolint:nilnil // (nil, nil) is http.Transport.Proxy's own contract for "no proxy"
		Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	directClient := &http.Client{Transport: noProxyTransport}

	bot, err := tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, directClient)
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
