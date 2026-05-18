package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/seilbekskindirov/monitor/internal/application/rulegen"
	integration "github.com/seilbekskindirov/monitor/internal/infrastructure/telegrambot"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// sentinel errors for parseRegenCommand — the dispatch handler maps each to a
// distinct user-facing reply without string matching.
var (
	errMissingSourceName     = errors.New("missing source name")
	errTooManyPositionalArgs = errors.New("too many positional arguments")
	errUnknownFlag           = errors.New("unknown flag")
	errInvalidMaxFallback    = errors.New("invalid --max-fallback value")
)

// parseRegenCommand splits the command text and extracts the source name + flags.
// Returns one of the package-private sentinel errors on malformed input; the
// caller maps each sentinel to a user-facing reply.
//
// Valid forms:
//
//	/regen <source>
//	/regen <source> --force-fallback
//	/regen <source> --max-fallback=N    (1 <= N <= 10)
//	/regen <source> --force-fallback --max-fallback=N
//
// Telegram may append @botname to the command token — it is stripped before comparison.
// The command word is case-insensitive; source names are treated case-sensitively.
func parseRegenCommand(text string) (string, bool, int, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false, 0, errMissingSourceName
	}
	// Strip @botname suffix from the first token (/regen@my_bot → /regen).
	head := fields[0]
	if i := strings.IndexByte(head, '@'); i >= 0 {
		head = head[:i]
	}
	if !strings.EqualFold(head, "/regen") {
		return "", false, 0, errMissingSourceName
	}
	if len(fields) < 2 {
		return "", false, 0, errMissingSourceName
	}
	sourceName := fields[1]

	var (
		forceFallback bool
		maxFallback   int
	)
	for _, tok := range fields[2:] {
		switch {
		case tok == "--force-fallback":
			forceFallback = true
		case strings.HasPrefix(tok, "--max-fallback="):
			raw := strings.TrimPrefix(tok, "--max-fallback=")
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 10 {
				return "", false, 0, errInvalidMaxFallback
			}
			maxFallback = n
		case strings.HasPrefix(tok, "--"):
			return "", false, 0, errUnknownFlag
		default:
			return "", false, 0, errTooManyPositionalArgs
		}
	}
	return sourceName, forceFallback, maxFallback, nil
}

// handleRegen handles the /regen command from the admin.
//
// Guards (in order):
//  1. Non-admin sender → silent fall-through (no reply).
//  2. Regen disabled (generator is nil) → polite reply.
//  3. Parse error → per-sentinel reply.
//  4. Lock contention → "already in progress" reply.
//  5. Send "Working on…" message; spawn goroutine for the long-running Generate call.
func (h *TelegramApi) handleRegen(ctx context.Context, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	// Guard 1: non-admin — silent ignore.
	if chatID != h.adminChatID {
		return
	}

	// Guard 2: regen disabled.
	if h.rulegenGenerator == nil {
		h.notifyText(ctx, chatID, "Rule regeneration is not enabled.")
		return
	}

	// Guard 3: parse command.
	sourceName, forceFallback, maxFallback, parseErr := parseRegenCommand(msg.Text)
	if parseErr != nil {
		reply := parseErrReply(parseErr)
		h.notifyText(ctx, chatID, reply)
		return
	}

	// Guard 4: lock contention.
	release, ok := h.rulegenLocks.TryAcquire(sourceName)
	if !ok {
		h.notifyText(ctx, chatID,
			fmt.Sprintf("Rule generation already in progress for <code>%s</code>.", html.EscapeString(sourceName)))
		return
	}

	// Step 5: send "Working on…" message and capture its id.
	workingText := fmt.Sprintf("Working on rules for <code>%s</code>…", html.EscapeString(sourceName))
	workingMsgID, sendErr := h.telegramClient.SendHTMLMessageReturning(ctx, integration.TelegramChatID(chatID), workingText)
	if sendErr != nil {
		log.Printf("telegram: regen send working msg chat=%d: %v", chatID, sendErr)
		release()
		return
	}

	// Step 6: spawn goroutine for the long-running generate call.
	go func() {
		defer release()

		// Pick the generator — use the factory when a custom maxFallback is requested.
		gen := h.rulegenGenerator
		if maxFallback > 0 {
			custom, factErr := h.generatorFactory(3, maxFallback)
			if factErr != nil {
				finalText := truncate(fmt.Sprintf("Failed to build generator: %s", html.EscapeString(factErr.Error())))
				if editErr := h.telegramClient.EditMessageText(context.Background(), integration.TelegramChatID(chatID), workingMsgID, finalText); editErr != nil {
					log.Printf("telegram: regen edit chat=%d msg=%d: %v", chatID, workingMsgID, editErr)
				}
				return
			}
			gen = custom
		}

		timeoutCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		result, genErr := gen.Generate(timeoutCtx, sourceName, forceFallback)

		var finalText string
		if genErr != nil {
			finalText = buildFailureReply(sourceName, genErr, timeoutCtx.Err())
		} else {
			finalText = buildSuccessReply(sourceName, result)
		}

		if editErr := h.telegramClient.EditMessageText(context.Background(), integration.TelegramChatID(chatID), workingMsgID, finalText); editErr != nil {
			log.Printf("telegram: regen edit chat=%d msg=%d: %v", chatID, workingMsgID, editErr)
		}
	}()
}

// parseErrReply maps parseRegenCommand sentinel errors to user-facing strings.
func parseErrReply(err error) string {
	switch {
	case errors.Is(err, errMissingSourceName):
		return "Usage: /regen &lt;source&gt; [--force-fallback] [--max-fallback=N]"
	case errors.Is(err, errTooManyPositionalArgs):
		return "Too many arguments. Expected exactly one source name."
	case errors.Is(err, errUnknownFlag):
		return "Unknown flag. Supported: --force-fallback, --max-fallback=N (1..10)."
	case errors.Is(err, errInvalidMaxFallback):
		return "--max-fallback must be an integer between 1 and 10."
	default:
		return "Invalid command."
	}
}

// buildSuccessReply formats the success HTML body for the bot reply.
func buildSuccessReply(sourceName string, result *rulegen.Result) string {
	escalated := "no fallback"
	if result.Escalated {
		escalated = "fallback"
	}
	body := fmt.Sprintf(
		"✅ <code>%s</code> → <b>%.4f</b>\nprovider: %s [%s]\nattempts: %d (%s)",
		html.EscapeString(sourceName),
		result.Value,
		html.EscapeString(result.Metadata.Provider),
		html.EscapeString(result.Metadata.Model),
		result.AttemptsUsed,
		escalated,
	)
	return truncate(body)
}

// buildFailureReply maps known rulegen errors to HTML failure bodies.
// Unknown errors are logged and mapped to an opaque message.
func buildFailureReply(sourceName string, err error, ctxErr error) string {
	escaped := html.EscapeString(sourceName)
	var body string
	switch {
	case ctxErr != nil && errors.Is(ctxErr, context.DeadlineExceeded):
		body = fmt.Sprintf("❌ <code>%s</code>: timed out after 120s", escaped)
	case errors.Is(err, rulegen.ErrSourceNotFound):
		body = fmt.Sprintf("❌ <code>%s</code>: source not found", escaped)
	case errors.Is(err, rulegen.ErrAttemptsExhausted):
		body = fmt.Sprintf("❌ <code>%s</code>: all attempts exhausted (primary=3, fallback=configured)", escaped)
	case errors.Is(err, rulegen.ErrUnsupportedFetcherKind):
		body = fmt.Sprintf("❌ <code>%s</code>: required fetcher not available in this build", escaped)
	default:
		log.Printf("telegram: regen unknown error for %q: %v", sourceName, err)
		body = fmt.Sprintf("❌ <code>%s</code>: internal error (see logs)", escaped)
	}
	return truncate(body)
}

// truncate defensively caps the message at 4000 characters to respect Telegram's
// 4096-char message limit with a small margin for HTML overhead.
func truncate(s string) string {
	const limit = 4000
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
