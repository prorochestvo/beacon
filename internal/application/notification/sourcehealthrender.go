package notification

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
)

// sourceHealthMaxErrorRunes caps how much of the upstream error reaches Telegram.
//
// Collection errors carry full URLs and wrapped transport messages and run to hundreds of
// characters; the alert needs enough to recognise the fault, not the whole trace, which is
// in execution_history and on /api/errors/execution for anyone who wants it.
const sourceHealthMaxErrorRunes = 300

// renderSourceDown builds the message for a source that has gone silent.
//
// It leads with what an operator needs to decide whether to act: which source, how long it
// has been quiet, and whether it is failing loudly or has stopped being attempted at all.
// Those two look identical from the outside and want completely different investigations —
// a broken selector versus a collector that is no longer reaching this source.
func renderSourceDown(
	source domain.RateSource,
	state domain.SourceCollectionHealth,
	now time.Time,
	threshold time.Duration,
) (string, string) {
	var b strings.Builder

	b.WriteString("⚠️ <b>Source silent</b>\n")
	fmt.Fprintf(&b, "<b>%s</b>", html.EscapeString(source.Title))
	if source.Title != source.Name {
		fmt.Fprintf(&b, " (<code>%s</code>)", html.EscapeString(source.Name))
	}
	b.WriteString("\n\n")

	if state.LastSuccessAt.IsZero() {
		fmt.Fprintf(&b, "Never collected successfully. First attempted %s ago.\n",
			humaniseDuration(now.Sub(state.LastRunAt)))
	} else {
		fmt.Fprintf(&b, "Last success: %s ago (%s)\n",
			humaniseDuration(now.Sub(state.LastSuccessAt)),
			state.LastSuccessAt.Format("2006-01-02 15:04 UTC"))
	}

	if state.ConsecutiveFailures > 0 {
		fmt.Fprintf(&b, "Failed attempts since: %d\n", state.ConsecutiveFailures)
	} else {
		// No failures recorded and yet no success either: the source is not being
		// attempted at all, which is a different problem from one that fails.
		b.WriteString("No attempts recorded since — the source is no longer being collected.\n")
	}

	fmt.Fprintf(&b, "Expected at least every %s (interval %s ×%d).\n",
		humaniseDuration(threshold), source.Interval, DefaultSourceStaleFactor)

	if state.LastError != "" {
		fmt.Fprintf(&b, "\n<b>Last error</b>\n<code>%s</code>",
			html.EscapeString(truncateRunes(state.LastError, sourceHealthMaxErrorRunes)))
	}

	return source.Name, b.String()
}

// renderSourceRecovered builds the message for a source that started working again.
//
// Short on purpose: the interesting message was the one that said it broke. This exists so
// an operator who saw that one is not left checking by hand whether it is still true.
func renderSourceRecovered(source domain.RateSource, state domain.SourceCollectionHealth, now time.Time) (string, string) {
	var b strings.Builder

	b.WriteString("✅ <b>Source recovered</b>\n")
	fmt.Fprintf(&b, "<b>%s</b>", html.EscapeString(source.Title))
	if source.Title != source.Name {
		fmt.Fprintf(&b, " (<code>%s</code>)", html.EscapeString(source.Name))
	}
	b.WriteString("\n\n")

	if state.LastSuccessAt.IsZero() {
		b.WriteString("Collecting again.")
		return source.Name, b.String()
	}

	fmt.Fprintf(&b, "Collecting again — last success %s ago (%s).",
		humaniseDuration(now.Sub(state.LastSuccessAt)),
		state.LastSuccessAt.Format("2006-01-02 15:04 UTC"))

	return source.Name, b.String()
}

// humaniseDuration renders a span the way an operator reads it — "3h 20m", not "3h20m0s".
// Precision drops as the span grows, because nobody triaging a two-day outage needs the
// minutes.
func humaniseDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) - days*24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, h)
	}
}

// truncateRunes cuts s to at most limit runes, marking that it was cut. Runes rather than
// bytes because upstream errors carry non-ASCII text and a byte cut can split one.
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
