package sourceaudit

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// WriteReport renders probe results to w and returns the count of non-OK
// results. When verbose, prints a per-source table plus summary; otherwise a
// single OK line on full success, or a FAIL summary plus MISS DETAILS on any
// failure.
func WriteReport(w io.Writer, results []ProbeResult, verbose bool) (failures int, err error) {
	uniqueURLs := make(map[string]struct{})
	for _, r := range results {
		uniqueURLs[r.URL] = struct{}{}
		if r.Status != StatusOK {
			failures++
		}
	}
	okCount := len(results) - failures

	if !verbose {
		if failures == 0 {
			line := fmt.Sprintf("OK: audited %d sources across %d URLs", len(results), len(uniqueURLs))
			if _, err = fmt.Fprintln(w, line); err != nil {
				return 0, err
			}
			return 0, nil
		}
		summary := fmt.Sprintf("FAIL: %d/%d sources MISS across %d URLs", failures, len(results), len(uniqueURLs))
		if _, err = fmt.Fprintln(w, summary); err != nil {
			return failures, err
		}
		if err = writeMissDetails(w, results); err != nil {
			return failures, err
		}
		return failures, nil
	}

	if err = writeTable(w, results); err != nil {
		return failures, err
	}
	summary := fmt.Sprintf("\naudited %d sources across %d URLs: %d OK, %d MISS",
		len(results), len(uniqueURLs), okCount, failures)
	if _, err = fmt.Fprintln(w, summary); err != nil {
		return failures, err
	}
	if failures > 0 {
		if err = writeMissDetails(w, results); err != nil {
			return failures, err
		}
	}
	return failures, nil
}

const urlMaxWidth = 72

func writeTable(w io.Writer, results []ProbeResult) error {
	nameW := len("name")
	urlW := len("url")
	valueW := len("value")
	statusW := len("status")

	for _, r := range results {
		n := displayName(r)
		if utf8.RuneCountInString(n) > nameW {
			nameW = utf8.RuneCountInString(n)
		}
		u := truncateURL(r.URL)
		if len(u) > urlW {
			urlW = len(u)
		}
		if len(r.Value) > valueW {
			valueW = len(r.Value)
		}
		if len(string(r.Status)) > statusW {
			statusW = len(string(r.Status))
		}
	}

	header := fmt.Sprintf("%-*s | %-*s | %-4s | %-*s | %s",
		nameW, "name",
		urlW, "url",
		"side",
		valueW, "value",
		"status",
	)
	sep := strings.Repeat("-", nameW+1) +
		"+" + strings.Repeat("-", urlW+2) +
		"+" + strings.Repeat("-", 6) +
		"+" + strings.Repeat("-", valueW+2) +
		"+" + strings.Repeat("-", statusW)

	if _, err := fmt.Fprintln(w, header); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, sep); err != nil {
		return err
	}

	for _, r := range results {
		line := fmt.Sprintf("%-*s | %-*s | %-4s | %-*s | %s",
			nameW, displayName(r),
			urlW, truncateURL(r.URL),
			r.Source.Side,
			valueW, r.Value,
			string(r.Status),
		)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func writeMissDetails(w io.Writer, results []ProbeResult) error {
	if _, err := fmt.Fprintln(w, "\nMISS DETAILS:"); err != nil {
		return err
	}
	for _, r := range results {
		if r.Status == StatusOK {
			continue
		}
		detail := strings.ReplaceAll(r.Detail, "\n", " | ")
		line := fmt.Sprintf("%s\t%s\t%s", displayName(r), string(r.Status), detail)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func displayName(r ProbeResult) string {
	if !r.Source.Active {
		return r.Source.Name + " [inactive]"
	}
	return r.Source.Name
}

func truncateURL(u string) string {
	if len(u) <= urlMaxWidth {
		return u
	}
	return u[:urlMaxWidth-3] + "..."
}
