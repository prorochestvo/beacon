package service

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/application/notification"
	"github.com/seilbekskindirov/monitor/internal/domain"
	integration "github.com/seilbekskindirov/monitor/internal/infrastructure/telegrambot"
)

// NewTelegramApi constructs a stateless TelegramApi handler. webAppURL is the
// fully-qualified https:// URL of the Telegram Mini App; when empty the WebApp
// keyboard button is omitted (safe for dev). profileRepo is optional — when nil
// the "Latest updates" digest renders timestamps in UTC; production always
// provides all five dependencies.
//
// The handler exposes only a read-only "Latest updates" view plus the Mini App
// launcher; subscription CRUD lives in the Mini App. Stale callback presses from
// removed buttons in older chat bubbles are acknowledged but ignored.
func NewTelegramApi(
	cltTelegram telegramClient,
	subRepo subscriptionRepository,
	rateValueRepo telegramRateValueRepository,
	sourceRepo telegramRateSourceRepository,
	profileRepo rateUserProfileRepository,
	webAppURL string,
) (*TelegramApi, error) {
	return &TelegramApi{
		telegramClient: cltTelegram,
		subRepo:        subRepo,
		rateValueRepo:  rateValueRepo,
		sourceRepo:     sourceRepo,
		profileRepo:    profileRepo,
		webAppURL:      webAppURL,
	}, nil
}

// TelegramApi serves the Telegram-side menu and a read-only "Latest updates"
// summary; subscription CRUD lives in the Mini App. The only outbound surfaces
// are the main menu and the latest-rates report.
type TelegramApi struct {
	telegramClient telegramClient
	subRepo        subscriptionRepository
	rateValueRepo  telegramRateValueRepository
	sourceRepo     telegramRateSourceRepository
	profileRepo    rateUserProfileRepository
	webAppURL      string
}

// telegramClient is the subset of the Telegram client surface this handler
// needs. *integration.TelegramBotClient satisfies it.
type telegramClient interface {
	Listen(context.Context, integration.UpdateHandler)
	SendPlainTextMessage(context.Context, integration.TelegramChatID, string) error
	SendMarkdownMessage(context.Context, integration.TelegramChatID, string) error
	SendHTMLMessage(context.Context, integration.TelegramChatID, string) error
	SendHTMLMessageReturning(context.Context, integration.TelegramChatID, string) (int, error)
	SendHTMLMessageWithKeyboard(context.Context, integration.TelegramChatID, string, tgbotapi.InlineKeyboardMarkup) error
	EditHTMLMessageWithKeyboard(context.Context, integration.TelegramChatID, int, string, tgbotapi.InlineKeyboardMarkup) error
	EditMessageText(context.Context, integration.TelegramChatID, int, string) error
	AnswerCallbackQuery(context.Context, string, string) error
}

type subscriptionRepository interface {
	ObtainRateUserSubscriptionsByUserID(ctx context.Context, userType domain.UserType, userID string) ([]domain.RateUserSubscription, error)
}

// telegramRateValueRepository is the narrow read-only interface for loading recent
// rate values needed by handleLatestUpdates.
type telegramRateValueRepository interface {
	ObtainLastNRateValuesBySourceName(ctx context.Context, name string, n int64) ([]domain.RateValue, error)
}

// telegramRateSourceRepository is the narrow read-only interface for batch-loading
// source metadata by name. One round-trip replaces per-subscription N+1 queries.
type telegramRateSourceRepository interface {
	ObtainRateSourcesByNames(ctx context.Context, names []string) (map[string]domain.RateSource, error)
}

// rateUserProfileRepository looks up per-user preferences such as timezone.
// Implementations return (nil, internal.ErrNotFound) when no row exists; that
// absence is normal and the handler treats it as "use UTC".
type rateUserProfileRepository interface {
	ObtainRateUserProfileByUserID(ctx context.Context, userType domain.UserType, userID string) (*domain.RateUserProfile, error)
}

// resolveUserTimezone returns the time.Location stored for userID, or nil when
// no profile is configured, the timezone name is unknown to the Go runtime, or
// the lookup fails. A nil return renders the digest in UTC. Failures are logged;
// a wrong timezone beats a missed digest.
//
// Mirrors RateCheckAgent.resolveUserTimezone but uses log.Printf since
// TelegramApi has no logger field. Kept separate intentionally (different
// logging surfaces, two callers).
func (h *TelegramApi) resolveUserTimezone(ctx context.Context, userID string) *time.Location {
	if h.profileRepo == nil {
		return nil
	}
	profile, err := h.profileRepo.ObtainRateUserProfileByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		if errors.Is(err, internal.ErrNotFound) {
			return nil
		}
		log.Printf("telegram: profile lookup chat_id=%s: %v", userID, err)
		return nil
	}
	if profile == nil || profile.Timezone == "" {
		return nil
	}
	loc, err := time.LoadLocation(profile.Timezone)
	if err != nil {
		log.Printf("telegram: unknown timezone chat_id=%s tz=%q: %v", userID, profile.Timezone, err)
		return nil
	}
	return loc
}

// Run starts the Telegram bot update loop in the background.
// It returns immediately; the loop runs until ctx is cancelled.
func (h *TelegramApi) Run(ctx context.Context) {
	handle := func(ctx context.Context, update tgbotapi.Update) {
		switch {
		case update.CallbackQuery != nil:
			cb := update.CallbackQuery
			log.Printf("telegram: update id=%d chat=%d kind=callback data=%q",
				update.UpdateID, cb.Message.Chat.ID, cb.Data)
			h.handleCallback(ctx, cb)
		case update.Message != nil:
			m := update.Message
			// Log metadata only; the body may contain PII or operator-supplied
			// tokens. The handler decides what to record about the content.
			log.Printf("telegram: update id=%d chat=%d kind=message text_len=%d",
				update.UpdateID, m.Chat.ID, len(m.Text))
			h.handleMessage(ctx, m)
		default:
			log.Printf("telegram: update id=%d kind=other", update.UpdateID)
		}
	}

	go h.telegramClient.Listen(ctx, handle)
}

// handleMessage replies with the main menu for every inbound message — slash
// commands and free-form text alike land on the same keyboard, with no "Please
// use /subscriptions" hint. Unknown slash commands are logged so an operator can
// see which command a user tried; free text is not logged to keep PII out of logs.
func (h *TelegramApi) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	lower := strings.TrimSpace(strings.ToLower(msg.Text))

	if strings.HasPrefix(lower, "/") && lower != commandSubscriptions && lower != commandStart {
		log.Printf("telegram: unknown command chat=%d cmd=%q", chatID, lower)
	}

	h.sendMainMenu(ctx, chatID, 0)
}

// handleCallback routes the two remaining inline-keyboard presses. Stale
// callback data from removed buttons in older chat bubbles is acknowledged (so
// the spinner clears) and otherwise ignored — no add/delete/show flows remain
// on the bot side.
func (h *TelegramApi) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	data := cb.Data

	// always acknowledge to clear the spinner
	h.ackCallback(ctx, cb.ID, "")

	switch data {
	case cbBack:
		h.sendMainMenu(ctx, chatID, msgID)
	case cbLatest:
		h.handleLatestUpdates(ctx, chatID, msgID)
	}
}

// sendMainMenu shows the top-level keyboard. When msgID > 0 the existing message
// is edited in place (callback flow); when 0 a new message is sent (text-command
// flow). The keyboard exposes only "Latest updates" and the WebApp launcher.
func (h *TelegramApi) sendMainMenu(ctx context.Context, chatID int64, msgID int) {
	rows := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📈 Latest updates", cbLatest),
		),
	}
	// WebApp button gets its own bottom row when a public URL is configured.
	// Telegram silently ignores WebApp buttons for non-anonymous bots in groups —
	// irrelevant here, this bot is DM-only.
	if h.webAppURL != "" {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			newWebAppButton("🌐 Open Mini App", h.webAppURL),
		))
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	const text = "<b>Subscription Management</b>\nChoose an action:"
	h.sendOrEditWithKeyboard(ctx, chatID, msgID, text, kb)
}

// handleLatestUpdates shows the current rate for each of the caller's subscriptions
// in the same aligned-table format the scheduled notifier emits. Sources are
// batch-loaded in one query; rate values are deduplicated by source name to avoid
// N+1 queries when a user has multiple subscriptions on one source.
//
// Inactive sources are included as long as a rate value row exists — a
// subscription to an inactive source remains a valid pair until deleted. A source
// removed from the DB entirely is silently skipped.
func (h *TelegramApi) handleLatestUpdates(ctx context.Context, chatID int64, msgID int) {
	userID := strconv.FormatInt(chatID, 10)

	subs, err := h.subRepo.ObtainRateUserSubscriptionsByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		h.notifyText(ctx, chatID, "⚠️ Failed to load subscriptions.")
		return
	}
	if len(subs) == 0 {
		h.sendOrEditWithKeyboard(ctx, chatID, msgID, "You have no subscriptions yet.", backKeyboard())
		return
	}

	// Collect the distinct set of source names so we can batch-load metadata.
	seen := make(map[string]struct{}, len(subs))
	names := make([]string, 0, len(subs))
	for _, s := range subs {
		if _, ok := seen[s.SourceName]; !ok {
			seen[s.SourceName] = struct{}{}
			names = append(names, s.SourceName)
		}
	}

	sourceMeta, err := h.sourceRepo.ObtainRateSourcesByNames(ctx, names)
	if err != nil {
		h.notifyText(ctx, chatID, "⚠️ Failed to load subscriptions.")
		return
	}

	// Deduplicate rate-value lookups by source name to avoid N+1 when the user
	// has multiple subscriptions on one source.
	currentPrices := make(map[string]float64, len(names))
	for _, name := range names {
		values, err := h.rateValueRepo.ObtainLastNRateValuesBySourceName(ctx, name, 1)
		if err != nil {
			log.Printf("telegram: rate value lookup source=%s chat=%s: %v", name, userID, err)
			continue
		}
		if len(values) == 0 {
			continue
		}
		currentPrices[name] = values[0].Price
	}

	// Build snapshots, silently skipping any sub whose source row is missing or
	// has no current price.
	snapshots := make([]notification.SubscriptionSnapshot, 0, len(subs))
	for _, sub := range subs {
		src, ok := sourceMeta[sub.SourceName]
		if !ok {
			continue
		}
		price, ok := currentPrices[sub.SourceName]
		if !ok {
			continue
		}
		snapshots = append(snapshots, notification.SubscriptionSnapshot{
			Subscription: sub,
			Source:       src,
			CurrentPrice: price,
		})
	}

	if len(snapshots) == 0 {
		// Has subscriptions but every one was filtered (no source row or no rate
		// data yet). Distinct message from the no-subscriptions case.
		log.Printf("telegram: no rate data chat=%s subs=%d snapshots=0", userID, len(subs))
		h.sendOrEditWithKeyboard(ctx, chatID, msgID, "No rate data available yet.", backKeyboard())
		return
	}

	loc := h.resolveUserTimezone(ctx, userID)
	parts, err := notification.BuildSubscriptionDigest(time.Now().UTC(), loc, snapshots)
	if err != nil {
		h.notifyText(ctx, chatID, "⚠️ Failed to load subscriptions.")
		return
	}

	h.sendDigestParts(ctx, chatID, msgID, parts)
}

// sendDigestParts delivers message parts to the chat. When msgID > 0 the first
// part edits the original callback message; subsequent parts and new-send flows
// use SendHTMLMessage. The «Back» keyboard attaches only to the last part.
func (h *TelegramApi) sendDigestParts(ctx context.Context, chatID int64, msgID int, parts []string) {
	if len(parts) == 0 {
		return
	}
	if len(parts) > 1 {
		log.Printf("telegram: digest parts=%d chat=%d", len(parts), chatID)
	}
	kb := backKeyboard()
	if len(parts) == 1 {
		h.sendOrEditWithKeyboard(ctx, chatID, msgID, parts[0], kb)
		return
	}

	// Multi-part: edit the original bubble with part[0] (when msgID > 0), then
	// send the remainder as new messages.
	first := parts[0]
	rest := parts[1:]

	if msgID > 0 {
		if err := h.telegramClient.EditMessageText(
			ctx, integration.TelegramChatID(chatID), msgID, first); err != nil {
			log.Printf("telegram: edit chat=%d msg=%d failed: %v", chatID, msgID, err)
		}
	} else {
		if err := h.telegramClient.SendHTMLMessage(
			ctx, integration.TelegramChatID(chatID), first); err != nil {
			log.Printf("telegram: send chat=%d failed: %v", chatID, err)
		}
	}

	for i, part := range rest {
		if i == len(rest)-1 {
			// Last part gets the keyboard.
			if err := h.telegramClient.SendHTMLMessageWithKeyboard(
				ctx, integration.TelegramChatID(chatID), part, kb); err != nil {
				log.Printf("telegram: send chat=%d failed: %v", chatID, err)
			}
		} else {
			if err := h.telegramClient.SendHTMLMessage(
				ctx, integration.TelegramChatID(chatID), part); err != nil {
				log.Printf("telegram: send chat=%d failed: %v", chatID, err)
			}
		}
	}
}

// sendOrEditWithKeyboard sends a new keyboard message when msgID is zero
// (text-command flow) or edits the existing message in place when msgID > 0
// (callback flow), avoiding a new chat bubble on every inline button press.
func (h *TelegramApi) sendOrEditWithKeyboard(ctx context.Context, chatID int64, msgID int, text string, kb tgbotapi.InlineKeyboardMarkup) {
	if msgID > 0 {
		if err := h.telegramClient.EditHTMLMessageWithKeyboard(
			ctx, integration.TelegramChatID(chatID), msgID, text, kb); err != nil {
			log.Printf("telegram: edit chat=%d msg=%d failed: %v", chatID, msgID, err)
		}
		return
	}
	if err := h.telegramClient.SendHTMLMessageWithKeyboard(
		ctx, integration.TelegramChatID(chatID), text, kb); err != nil {
		log.Printf("telegram: send chat=%d failed: %v", chatID, err)
	}
}

// notifyText sends a plain HTML message and logs delivery failures. Used for
// one-shot user notifications where the caller has no recovery path.
func (h *TelegramApi) notifyText(ctx context.Context, chatID int64, text string) {
	if err := h.telegramClient.SendHTMLMessage(
		ctx, integration.TelegramChatID(chatID), text); err != nil {
		log.Printf("telegram: notify chat=%d failed: %v", chatID, err)
	}
}

// ackCallback acknowledges a callback_query, clearing the spinner; logs delivery failures.
func (h *TelegramApi) ackCallback(ctx context.Context, callbackID, text string) {
	if err := h.telegramClient.AnswerCallbackQuery(ctx, callbackID, text); err != nil {
		log.Printf("telegram: ack callback=%s failed: %v", callbackID, err)
	}
}

const (
	cbLatest = "sub:latest"
	cbBack   = "sub:back"

	commandStart         = "/start"
	commandSubscriptions = "/subscriptions"
)

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
