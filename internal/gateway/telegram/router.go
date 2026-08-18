// Package telegram is the Telegram half of the gateway: it consumes the updates
// the infrastructure client polls, decides what each one means, and renders the
// reply.
//
// Everything here is receiving and presentation. What the bot has to *say* — the
// button captions, the empty-state wording, the choice between editing a bubble
// and sending a new one — lives in this package; what the answer *is* comes from
// the application layer. The split is what lets a move to webhooks replace the
// update source without touching a handler.
package telegram

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	appdigest "github.com/seilbekskindirov/beacon/internal/application/digest"
	integration "github.com/seilbekskindirov/beacon/internal/infrastructure/telegrambot"
)

// Config carries every dependency a Router needs.
//
// Named fields rather than a positional list: Bot and Digest are both interfaces
// and WebAppURL is a string that is legitimately empty, so a transposed pair
// would compile and only misbehave in a chat.
type Config struct {
	// Bot is the update source and the send surface. Required.
	Bot BotClient
	// Digest answers the "Latest updates" button. Required.
	Digest DigestService
	// WebAppURL is the https:// URL of the Mini App. Empty omits the launcher
	// button, which is how a deployment without a public origin is meant to
	// behave — Telegram rejects a WebApp button pointing anywhere else.
	WebAppURL string
	// Logger receives delivery failures and the update trace. Defaults to
	// log.Default() when nil.
	Logger *log.Logger
}

// Router dispatches Telegram updates onto the bot's two surfaces: the main menu
// and the read-only "Latest updates" summary. Subscription editing lives in the
// Mini App, so nothing here writes.
type Router struct {
	bot       BotClient
	digest    DigestService
	webAppURL string
	logger    *log.Logger
}

// NewRouter constructs a Router from cfg, or reports every required dependency
// cfg left nil.
func NewRouter(cfg Config) (*Router, error) {
	required := []struct {
		name    string
		present bool
	}{
		{"Bot", cfg.Bot != nil},
		{"Digest", cfg.Digest != nil},
	}
	var missing []string
	for _, dep := range required {
		if !dep.present {
			missing = append(missing, dep.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("telegram: config is missing %s", strings.Join(missing, ", "))
	}

	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}

	return &Router{
		bot:       cfg.Bot,
		digest:    cfg.Digest,
		webAppURL: cfg.WebAppURL,
		logger:    logger,
	}, nil
}

// Run consumes updates until ctx is cancelled. It blocks — run it in a goroutine.
//
// The loop ends when the source channel closes, which the client does on
// cancellation, so shutdown needs no second signal here.
func (r *Router) Run(ctx context.Context) {
	for update := range r.bot.Updates(ctx) {
		r.dispatch(ctx, update)
	}
}

// dispatch routes one update and records that it arrived.
//
// The log line carries metadata only. A message body may hold anything the user
// typed, and the handler below is what decides which part of it is safe to
// record.
func (r *Router) dispatch(ctx context.Context, update tgbotapi.Update) {
	switch {
	case update.CallbackQuery != nil:
		cb := update.CallbackQuery
		r.logger.Printf("telegram: update id=%d chat=%d kind=callback data=%q",
			update.UpdateID, cb.Message.Chat.ID, cb.Data)
		r.handleCallback(ctx, cb)
	case update.Message != nil:
		m := update.Message
		r.logger.Printf("telegram: update id=%d chat=%d kind=message text_len=%d",
			update.UpdateID, m.Chat.ID, len(m.Text))
		r.handleMessage(ctx, m)
	default:
		r.logger.Printf("telegram: update id=%d kind=other", update.UpdateID)
	}
}

// handleMessage replies with the main menu for every inbound message — slash
// commands and free-form text alike land on the same keyboard, with no "Please
// use /subscriptions" hint. Unknown slash commands are logged so an operator can
// see which command a user tried; free text is not logged, to keep PII out.
func (r *Router) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	lower := strings.TrimSpace(strings.ToLower(msg.Text))

	if strings.HasPrefix(lower, "/") && lower != commandSubscriptions && lower != commandStart {
		r.logger.Printf("telegram: unknown command chat=%d cmd=%q", chatID, lower)
	}

	r.sendMainMenu(ctx, chatID, 0)
}

// handleCallback routes the two remaining inline-keyboard presses. Stale callback
// data from removed buttons in older chat bubbles is acknowledged (so the spinner
// clears) and otherwise ignored — no add/delete/show flows remain on the bot side.
func (r *Router) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID

	// Always acknowledge first, so the spinner clears even on a press this router
	// no longer recognises.
	r.ackCallback(ctx, cb.ID, "")

	switch cb.Data {
	case cbBack:
		r.sendMainMenu(ctx, chatID, msgID)
	case cbLatest:
		r.sendLatestUpdates(ctx, chatID, msgID)
	}
}

// sendMainMenu shows the top-level keyboard. When msgID > 0 the existing message
// is edited in place (callback flow); when 0 a new message is sent (text-command
// flow). The keyboard exposes only "Latest updates" and the WebApp launcher.
func (r *Router) sendMainMenu(ctx context.Context, chatID int64, msgID int) {
	rows := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📈 Latest updates", cbLatest),
		),
	}
	// WebApp button gets its own bottom row when a public URL is configured.
	// Telegram silently ignores WebApp buttons for non-anonymous bots in groups —
	// irrelevant here, this bot is DM-only.
	if r.webAppURL != "" {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			newWebAppButton("🌐 Open Mini App", r.webAppURL),
		))
	}
	r.sendOrEditWithKeyboard(ctx, chatID, msgID, textMainMenu, tgbotapi.NewInlineKeyboardMarkup(rows...))
}

// sendLatestUpdates asks the application layer for the caller's digest and
// renders whichever of its three outcomes came back.
//
// The counts, not the parts, separate "you have nothing" from "nothing has been
// collected yet": an empty Parts alone cannot tell them apart, and the two want
// different words.
func (r *Router) sendLatestUpdates(ctx context.Context, chatID int64, msgID int) {
	d, err := r.digest.ObtainMeDigest(ctx, strconv.FormatInt(chatID, 10))
	if err != nil {
		// The user gets a warning either way; the reason belongs in the log, or a
		// dead repository looks the same as an empty account.
		r.logger.Printf("telegram: digest chat=%d failed: %v", chatID, err)
		r.notifyText(ctx, chatID, textDigestFailed)
		return
	}

	switch {
	case d.Subscriptions == 0:
		r.sendOrEditWithKeyboard(ctx, chatID, msgID, textNoSubscriptions, backKeyboard())
	case len(d.Parts) == 0:
		r.sendOrEditWithKeyboard(ctx, chatID, msgID, textNoRateData, backKeyboard())
	default:
		r.sendDigestParts(ctx, chatID, msgID, d.Parts)
	}
}

// sendDigestParts delivers message parts to the chat. When msgID > 0 the first
// part edits the original callback message; subsequent parts and new-send flows
// use SendHTMLMessage. The «Back» keyboard attaches only to the last part.
func (r *Router) sendDigestParts(ctx context.Context, chatID int64, msgID int, parts []string) {
	if len(parts) == 0 {
		return
	}
	if len(parts) > 1 {
		r.logger.Printf("telegram: digest parts=%d chat=%d", len(parts), chatID)
	}
	kb := backKeyboard()
	if len(parts) == 1 {
		r.sendOrEditWithKeyboard(ctx, chatID, msgID, parts[0], kb)
		return
	}

	// Multi-part: edit the original bubble with part[0] (when msgID > 0), then
	// send the remainder as new messages.
	first, rest := parts[0], parts[1:]

	if msgID > 0 {
		if err := r.bot.EditMessageText(
			ctx, integration.TelegramChatID(chatID), msgID, first); err != nil {
			r.logger.Printf("telegram: edit chat=%d msg=%d failed: %v", chatID, msgID, err)
		}
	} else if err := r.bot.SendHTMLMessage(
		ctx, integration.TelegramChatID(chatID), first); err != nil {
		r.logger.Printf("telegram: send chat=%d failed: %v", chatID, err)
	}

	for i, part := range rest {
		if i == len(rest)-1 {
			// Last part gets the keyboard.
			if err := r.bot.SendHTMLMessageWithKeyboard(
				ctx, integration.TelegramChatID(chatID), part, kb); err != nil {
				r.logger.Printf("telegram: send chat=%d failed: %v", chatID, err)
			}
			continue
		}
		if err := r.bot.SendHTMLMessage(
			ctx, integration.TelegramChatID(chatID), part); err != nil {
			r.logger.Printf("telegram: send chat=%d failed: %v", chatID, err)
		}
	}
}

// sendOrEditWithKeyboard sends a new keyboard message when msgID is zero
// (text-command flow) or edits the existing message in place when msgID > 0
// (callback flow), avoiding a new chat bubble on every inline button press.
func (r *Router) sendOrEditWithKeyboard(ctx context.Context, chatID int64, msgID int, text string, kb tgbotapi.InlineKeyboardMarkup) {
	if msgID > 0 {
		if err := r.bot.EditHTMLMessageWithKeyboard(
			ctx, integration.TelegramChatID(chatID), msgID, text, kb); err != nil {
			r.logger.Printf("telegram: edit chat=%d msg=%d failed: %v", chatID, msgID, err)
		}
		return
	}
	if err := r.bot.SendHTMLMessageWithKeyboard(
		ctx, integration.TelegramChatID(chatID), text, kb); err != nil {
		r.logger.Printf("telegram: send chat=%d failed: %v", chatID, err)
	}
}

// notifyText sends a plain HTML message and logs delivery failures. Used for
// one-shot notifications where the caller has no recovery path.
func (r *Router) notifyText(ctx context.Context, chatID int64, text string) {
	if err := r.bot.SendHTMLMessage(
		ctx, integration.TelegramChatID(chatID), text); err != nil {
		r.logger.Printf("telegram: notify chat=%d failed: %v", chatID, err)
	}
}

// ackCallback acknowledges a callback_query, clearing the spinner; logs delivery failures.
func (r *Router) ackCallback(ctx context.Context, callbackID, text string) {
	if err := r.bot.AnswerCallbackQuery(ctx, callbackID, text); err != nil {
		r.logger.Printf("telegram: ack callback=%s failed: %v", callbackID, err)
	}
}

const (
	cbLatest = "sub:latest"
	cbBack   = "sub:back"

	commandStart         = "/start"
	commandSubscriptions = "/subscriptions"
)

// The bot's whole vocabulary. It lives here rather than in the application layer
// because which words meet a user is presentation: the digest reports that the
// account is empty, this decides how to say so.
const (
	textMainMenu        = "<b>Subscription Management</b>\nChoose an action:"
	textNoSubscriptions = "You have no subscriptions yet."
	textNoRateData      = "No rate data available yet."
	textDigestFailed    = "⚠️ Failed to load subscriptions."
)

// BotClient is the subset of the Telegram client surface this router needs:
// where updates come from, and the four ways it answers.
// *integration.TelegramBotClient satisfies it.
type BotClient interface {
	Updates(ctx context.Context) <-chan tgbotapi.Update
	SendHTMLMessage(context.Context, integration.TelegramChatID, string) error
	SendHTMLMessageWithKeyboard(context.Context, integration.TelegramChatID, string, tgbotapi.InlineKeyboardMarkup) error
	EditHTMLMessageWithKeyboard(context.Context, integration.TelegramChatID, int, string, tgbotapi.InlineKeyboardMarkup) error
	EditMessageText(context.Context, integration.TelegramChatID, int, string) error
	AnswerCallbackQuery(context.Context, string, string) error
}

// DigestService is the application service behind the "Latest updates" button,
// satisfied by *appdigest.Service. Loading, pricing and rendering the table live
// there; choosing the words for each outcome lives here.
type DigestService interface {
	ObtainMeDigest(ctx context.Context, userID string) (appdigest.Digest, error)
}

// backKeyboard is the single-button keyboard that returns to the main menu.
func backKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("« Back", cbBack),
		),
	)
}

// newWebAppButton builds an inline keyboard button that opens the Telegram Mini App.
// Uses the Bot API 6.0+ WebApp button type so Telegram injects initData into the page.
func newWebAppButton(text, webAppURL string) tgbotapi.InlineKeyboardButton {
	return tgbotapi.NewInlineKeyboardButtonWebApp(text, tgbotapi.WebAppInfo{URL: webAppURL})
}
