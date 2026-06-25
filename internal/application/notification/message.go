package notification

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/tools/labelfmt"
)

const (
	telegramMaxMessageLen = 2048

	// hashtagDelta is the per-trigger hashtag emitted when a delta condition
	// fired. Uppercase ASCII so it renders as one clickable token in every
	// Telegram client and lets users filter chat history by trigger type.
	hashtagDelta = "#DELTA"

	// hashtagInterval/hashtagDaily/hashtagCron tag time-based triggers
	// separately (not collapsed to one tag) so users can distinguish e.g. a
	// daily 09:00 alert from a fixed-interval one.
	hashtagInterval = "#INTERVAL"
	hashtagDaily    = "#DAILY"
	hashtagCron     = "#CRON"

	// arrowUp is the in-table arrow for a positive delta.
	arrowUp = "↑" // U+2191

	// arrowDown is the in-table arrow for a negative delta.
	arrowDown = "↓" // U+2193

	// minusSign is the U+2212 MINUS SIGN used in the delta and value columns so
	// it aligns visually with the ASCII '+' for positive values.
	minusSign = "−" // U+2212
)

// alert carries the data for one row in the notification table.
// BaseCurrency and QuoteCurrency are HTML-escaped in pairLabel before insertion
// into the <pre> block, so odd source codes cannot inject HTML.
// SourceName is stored pre-escaped (escaped at assignment in foldIntoBuckets);
// if rendered directly into HTML it must not be escaped again.
type alert struct {
	SourceName    string
	BaseCurrency  string                // e.g. "USD"
	QuoteCurrency string                // e.g. "KZT"
	CurrencyKind  domain.RateSourceKind // BID or ASK
	CurrentPrice  float64               // newest price, e.g. 470.46
	Delta         float64               // signed delta: positive = up, negative = down
	Triggers      []alertTrigger        // ordered: delta, interval, daily, cron
}

// alertTrigger records which condition type (and its collapsed value) caused a
// notification to fire.
type alertTrigger struct {
	ConditionType  domain.SubscriptionConditionType
	ConditionValue string
}

// buildAlertMessage renders alerts into one or more Telegram HTML message parts.
// now is the run timestamp used verbatim in the header — the clock is injected,
// never read here. loc is the render timezone; nil falls back to UTC.
// Returns an empty slice when alerts is empty.
func buildAlertMessage(now time.Time, loc *time.Location, alerts ...alert) ([]string, error) {
	if len(alerts) == 0 {
		return nil, nil
	}

	sort.Slice(alerts, func(i, j int) bool {
		pi := pairLabel(alerts[i])
		pj := pairLabel(alerts[j])
		return pi < pj
	})

	rows := buildRows(alerts)
	return splitIntoParts(now, loc, rows, alerts), nil
}

// pairLabel returns the display pair string for a row (BID → base/quote, ASK → quote/base).
// Each currency code is HTML-escaped so odd source codes cannot inject HTML into the <pre> block.
func pairLabel(a alert) string {
	base := html.EscapeString(a.BaseCurrency)
	quote := html.EscapeString(a.QuoteCurrency)
	if a.CurrencyKind == domain.RateSourceKindBID {
		return fmt.Sprintf("%s/%s", base, quote)
	}
	return fmt.Sprintf("%s/%s", quote, base)
}

// tableRow holds pre-rendered column strings for one alert row.
type tableRow struct {
	pair  string // e.g. "USD/KZT" or "KZT/USD"
	value string // e.g. "68 382.56"
	delta string // e.g. "+2.60" or "−74.79", or "" when suppressed
	arrow string // "↑", "↓", or "" when suppressed
}

// buildRows renders alerts into tableRow values, applying the first-fire guard
// (Delta == 0 || Delta == CurrentPrice → blank delta+arrow cells).
func buildRows(alerts []alert) []tableRow {
	rows := make([]tableRow, len(alerts))
	for i, a := range alerts {
		row := tableRow{
			pair:  pairLabel(a),
			value: labelfmt.GroupThousands(a.CurrentPrice),
		}
		if a.Delta != 0 && a.Delta != a.CurrentPrice {
			if a.Delta > 0 {
				row.delta = fmt.Sprintf("+%s", labelfmt.GroupThousands(a.Delta))
				row.arrow = arrowUp
			} else {
				row.delta = fmt.Sprintf("%s%s", minusSign, labelfmt.GroupThousands(-a.Delta))
				row.arrow = arrowDown
			}
		}
		rows[i] = row
	}
	return rows
}

// renderBlock formats tableRow values into an aligned text block ready to wrap
// in <pre>…</pre>. Column separator is 2 spaces. Widths are computed by rune
// count (not bytes) so multibyte characters align. Trailing whitespace is trimmed.
func renderBlock(rows []tableRow) string {
	if len(rows) == 0 {
		return ""
	}
	var pairW, valueW, deltaW int
	for _, r := range rows {
		if w := utf8.RuneCountInString(r.pair); w > pairW {
			pairW = w
		}
		if w := utf8.RuneCountInString(r.value); w > valueW {
			valueW = w
		}
		if w := utf8.RuneCountInString(r.delta); w > deltaW {
			deltaW = w
		}
	}

	var sb strings.Builder
	for _, r := range rows {
		pairPad := pairW - utf8.RuneCountInString(r.pair)
		valuePad := valueW - utf8.RuneCountInString(r.value)
		deltaPad := deltaW - utf8.RuneCountInString(r.delta)

		line := r.pair + strings.Repeat(" ", pairPad) +
			"  " + strings.Repeat(" ", valuePad) + r.value +
			"  " + strings.Repeat(" ", deltaPad) + r.delta +
			" " + r.arrow
		sb.WriteString(strings.TrimRight(line, " "))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// reasonHashtags returns the header-line hashtag prefix for a message part:
// one hashtag per distinct trigger type fired across partAlerts. Order is
// canonical — #DELTA first when present, then schedule-family alphabetically
// (#CRON, #DAILY, #INTERVAL) — so tests stay stable and the user's chat
// history does not visually reshuffle when the same alert recurs.
//
// Returns "" when no trigger types are present (e.g. a first-fire row with no
// Triggers); the header still renders "FX rates" without a leading hashtag.
func reasonHashtags(partAlerts []alert) string {
	var hasDelta, hasInterval, hasDaily, hasCron bool
	for _, a := range partAlerts {
		for _, tr := range a.Triggers {
			switch tr.ConditionType {
			case domain.ConditionTypeDelta:
				hasDelta = true
			case domain.ConditionTypeInterval:
				hasInterval = true
			case domain.ConditionTypeDaily:
				hasDaily = true
			case domain.ConditionTypeCron:
				hasCron = true
			}
		}
	}
	var tags []string
	if hasDelta {
		tags = append(tags, hashtagDelta)
	}
	if hasCron {
		tags = append(tags, hashtagCron)
	}
	if hasDaily {
		tags = append(tags, hashtagDaily)
	}
	if hasInterval {
		tags = append(tags, hashtagInterval)
	}
	return strings.Join(tags, " ")
}

// headerLines returns the two header lines for a message part.
//
// Line 1: "#TAG1 #TAG2 FX rates", or just "FX rates" when partAlerts carry no
// triggers. The hashtag prefix lets users filter history by trigger type.
//
// Line 2: the timestamp in loc with a numeric offset suffix, e.g.
// "Sun 24 May, 14:57 +05". loc=nil falls back to UTC.
func headerLines(now time.Time, loc *time.Location, partAlerts []alert) string {
	if loc == nil {
		loc = time.UTC
	}
	ts := now.In(loc).Format("Mon 2 Jan, 15:04 -07")
	title := "FX rates"
	if tags := reasonHashtags(partAlerts); tags != "" {
		title = tags + " " + title
	}
	return title + "\n" + ts
}

// splitIntoParts packs rows into message parts bounded by telegramMaxMessageLen,
// re-emitting the header and a balanced <pre>…</pre> block per part with widths
// recomputed per part. A single oversized row is still emitted as its own part
// (Telegram rejects it, but the loop must not spin forever).
func splitIntoParts(now time.Time, loc *time.Location, rows []tableRow, alerts []alert) []string {
	var parts []string
	start := 0

	for start < len(rows) {
		end := start + 1 // always include at least one row
		for end < len(rows) {
			candidate := buildPart(now, loc, rows[start:end+1], alerts[start:end+1])
			if len(candidate) > telegramMaxMessageLen {
				break
			}
			end++
		}
		parts = append(parts, buildPart(now, loc, rows[start:end], alerts[start:end]))
		start = end
	}
	return parts
}

// buildPart assembles one complete message part for the given row and alert slices.
func buildPart(now time.Time, loc *time.Location, rows []tableRow, partAlerts []alert) string {
	header := headerLines(now, loc, partAlerts)
	block := renderBlock(rows)
	return header + "\n\n<pre>\n" + block + "\n</pre>"
}
