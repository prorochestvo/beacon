package sourceaudit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/seilbekskindirov/monitor/internal/domain"
)

// SeededSource represents a rate source record parsed from a seed SQL file.
type SeededSource struct {
	Name     string
	Vendor   string
	Base     string
	Quote    string
	URL      string
	Interval string
	Side     string
	Active   bool
	Rules    []domain.RateSourceRule
	Origin   string
}

// ParseSeedFiles reads all files in fsys matching glob (lexicographic order) and
// returns the parsed SeededSource records in the order they appear.
func ParseSeedFiles(fsys fs.FS, glob string) ([]SeededSource, error) {
	matches, err := fs.Glob(fsys, glob)
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", glob, err)
	}

	sort.Strings(matches)

	var out []SeededSource
	for _, path := range matches {
		records, err := parseSeedFile(fsys, path)
		if err != nil {
			return nil, err
		}
		out = append(out, records...)
	}
	return out, nil
}

// insertForm classifies a SQL line as one of three recognized shapes.
type insertForm int

const (
	noMatch    insertForm = iota
	positional            // INSERT OR IGNORE INTO rate_sources VALUES(...)
	columnList            // INSERT OR IGNORE INTO rate_sources (cols) VALUES(...)
)

// legacyPositionalColumns defines the column order for positional INSERT rows.
// Rows with 10 tokens use the first 10; rows with 12 tokens use all 12.
var legacyPositionalColumns = []string{
	"name", "title", "base_currency", "quote_currency",
	"url", "interval", "kind", "active", "options", "rules",
	"rule_metadata", "fetcher_kind",
}

var (
	// reInsertColumnList matches: INSERT OR IGNORE INTO rate_sources (cols) VALUES (vals);
	// Column-list is tried first (longer match) to avoid false positives on the positional form.
	reInsertColumnList = regexp.MustCompile(
		`^INSERT\s+OR\s+IGNORE\s+INTO\s+rate_sources\s*\(([^)]*)\)\s+VALUES\s*\((.*)\)\s*;\s*$`)
	// reInsertPositional matches: INSERT OR IGNORE INTO rate_sources VALUES(vals);
	reInsertPositional = regexp.MustCompile(
		`^INSERT\s+OR\s+IGNORE\s+INTO\s+rate_sources\s+VALUES\s*\((.*)\)\s*;\s*$`)
	// reLooksLikeInsert detects a line with the insert prefix that matches neither
	// valid form — the loud-fail guard against the original silent-skip bug.
	reLooksLikeInsert = regexp.MustCompile(
		`^INSERT\s+OR\s+IGNORE\s+INTO\s+rate_sources\b`)
)

// stderrLogger writes to stderr because cmd/doctor audit prints its report to
// stdout; mixing channels would corrupt machine-readable output.
var stderrLogger = log.New(os.Stderr, "", 0)

// recogniseInsert classifies line and returns the relevant parenthesised
// payloads. For columnList, columnsPayload holds the column-name list and
// valuesPayload holds the value tokens. For positional, valuesPayload holds
// the value tokens and columnsPayload is empty.
func recogniseInsert(line string) (form insertForm, columnsPayload, valuesPayload string) {
	if m := reInsertColumnList.FindStringSubmatch(line); m != nil {
		return columnList, m[1], m[2]
	}
	if m := reInsertPositional.FindStringSubmatch(line); m != nil {
		return positional, "", m[1]
	}
	return noMatch, "", ""
}

func parseSeedFile(fsys fs.FS, path string) ([]SeededSource, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func(c fs.File) { _ = c.Close() }(f)

	base := filepath.Base(path)
	var out []SeededSource
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}

		form, colsPayload, valsPayload := recogniseInsert(line)
		switch form {
		case noMatch:
			// A rate_sources INSERT matching neither valid form fails loudly —
			// the regression guard for the original silent-skip bug.
			if reLooksLikeInsert.MatchString(line) {
				return nil, fmt.Errorf("%s:%d: malformed INSERT OR IGNORE INTO rate_sources statement", base, lineNum)
			}
			// Other tables, DDL, blank lines, comments — silently skip.
			continue

		case positional:
			tokens, err := tokenizeSQLValues(valsPayload)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: tokenize: %w", base, lineNum, err)
			}
			colMap, err := positionalToColumnMap(tokens, base, lineNum)
			if err != nil {
				return nil, err
			}
			src, err := seededSourceFromColumns(colMap, base, lineNum)
			if err != nil {
				return nil, err
			}
			src.Origin = fmt.Sprintf("%s:%d", base, lineNum)
			out = append(out, src)

		case columnList:
			colNames, err := parseColumnList(colsPayload, base, lineNum)
			if err != nil {
				return nil, err
			}
			tokens, err := tokenizeSQLValues(valsPayload)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: tokenize values: %w", base, lineNum, err)
			}
			if len(colNames) != len(tokens) {
				return nil, fmt.Errorf("%s:%d: column count %d does not match value count %d",
					base, lineNum, len(colNames), len(tokens))
			}
			colMap := make(map[string]string, len(colNames))
			for i, name := range colNames {
				colMap[name] = tokens[i]
			}
			src, err := seededSourceFromColumns(colMap, base, lineNum)
			if err != nil {
				return nil, err
			}
			src.Origin = fmt.Sprintf("%s:%d", base, lineNum)
			out = append(out, src)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}

// parseColumnList splits a comma-separated SQL column-name list and returns
// the trimmed identifiers. Returns an error if the list is empty or any name
// is empty after trimming.
func parseColumnList(payload, base string, lineNum int) ([]string, error) {
	parts := strings.Split(payload, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			return nil, fmt.Errorf("%s:%d: malformed column list: empty column name in %q", base, lineNum, payload)
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s:%d: malformed column list: no columns found", base, lineNum)
	}
	return names, nil
}

// positionalToColumnMap maps positional tokens to legacyPositionalColumns.
// Accepts 10 tokens (pre-009 rows) or 12 (post-010 rows); other arities are
// rejected.
func positionalToColumnMap(tokens []string, base string, lineNum int) (map[string]string, error) {
	switch len(tokens) {
	case 10, 12:
	default:
		return nil, fmt.Errorf("%s:%d: expected 10 or 12 columns for positional INSERT, got %d", base, lineNum, len(tokens))
	}
	colMap := make(map[string]string, len(tokens))
	for i, tok := range tokens {
		colMap[legacyPositionalColumns[i]] = tok
	}
	// Fill defaults for missing trailing columns when only 10 tokens provided.
	if len(tokens) == 10 {
		colMap["rule_metadata"] = "'{}'"
		colMap["fetcher_kind"] = "'plain'"
	}
	return colMap, nil
}

// seededSourceFromColumns extracts a SeededSource from the name-to-raw-token
// map. Unknown columns warn to stderr but do not error; missing required
// columns error.
func seededSourceFromColumns(colMap map[string]string, base string, lineNum int) (SeededSource, error) {
	known := map[string]bool{
		"name": true, "title": true, "base_currency": true, "quote_currency": true,
		"url": true, "interval": true, "kind": true, "active": true,
		"options": true, "rules": true, "rule_metadata": true, "fetcher_kind": true,
	}
	for col := range colMap {
		if !known[col] {
			stderrLogger.Printf("sourceaudit: %s:%d: ignoring unknown column %q", base, lineNum, col)
		}
	}

	name, err := requireUnquotedString(colMap, "name", base, lineNum)
	if err != nil {
		return SeededSource{}, err
	}
	vendor, err := requireUnquotedString(colMap, "title", base, lineNum)
	if err != nil {
		return SeededSource{}, err
	}
	baseCurrency, err := requireUnquotedString(colMap, "base_currency", base, lineNum)
	if err != nil {
		return SeededSource{}, err
	}
	quoteCurrency, err := requireUnquotedString(colMap, "quote_currency", base, lineNum)
	if err != nil {
		return SeededSource{}, err
	}
	rawURL, err := requireUnquotedString(colMap, "url", base, lineNum)
	if err != nil {
		return SeededSource{}, err
	}
	interval, err := requireUnquotedString(colMap, "interval", base, lineNum)
	if err != nil {
		return SeededSource{}, err
	}
	side, err := requireUnquotedString(colMap, "kind", base, lineNum)
	if err != nil {
		return SeededSource{}, err
	}

	activeTok, ok := colMap["active"]
	if !ok {
		return SeededSource{}, fmt.Errorf("%s:%d: missing required column %q", base, lineNum, "active")
	}
	activeInt, err := strconv.Atoi(strings.TrimSpace(activeTok))
	if err != nil {
		return SeededSource{}, fmt.Errorf("%s:%d: column active: %w", base, lineNum, err)
	}
	var active bool
	switch activeInt {
	case 1:
		active = true
	case 0:
		active = false
	default:
		return SeededSource{}, fmt.Errorf("%s:%d: column active: unexpected value %d (must be 0 or 1)", base, lineNum, activeInt)
	}

	// options is optional for SeededSource; parse to validate but do not surface.
	if _, hasOptions := colMap["options"]; hasOptions {
		if _, err = sqlUnquote(colMap["options"]); err != nil {
			return SeededSource{}, fmt.Errorf("%s:%d: column options: %w", base, lineNum, err)
		}
	}

	rulesRaw, err := requireUnquotedString(colMap, "rules", base, lineNum)
	if err != nil {
		return SeededSource{}, err
	}
	var rules []domain.RateSourceRule
	if err = json.Unmarshal([]byte(rulesRaw), &rules); err != nil {
		return SeededSource{}, fmt.Errorf("%s:%d: column rules: decode JSON: %w", base, lineNum, err)
	}

	return SeededSource{
		Name:     name,
		Vendor:   vendor,
		Base:     baseCurrency,
		Quote:    quoteCurrency,
		URL:      rawURL,
		Interval: interval,
		Side:     side,
		Active:   active,
		Rules:    rules,
	}, nil
}

// requireUnquotedString looks up column in colMap and SQL-unquotes its value.
// Returns an error if the column is absent or the value is not a quoted string.
func requireUnquotedString(colMap map[string]string, column, base string, lineNum int) (string, error) {
	tok, ok := colMap[column]
	if !ok {
		return "", fmt.Errorf("%s:%d: missing required column %q", base, lineNum, column)
	}
	v, err := sqlUnquote(tok)
	if err != nil {
		return "", fmt.Errorf("%s:%d: column %s: %w", base, lineNum, column, err)
	}
	return v, nil
}

// tokenizeSQLValues splits a comma-separated SQL value list respecting
// single-quoted strings (with ” escape sequences). Returns raw tokens including quotes.
func tokenizeSQLValues(s string) ([]string, error) {
	var out []string
	var buf strings.Builder
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inString && c == '\'' && i+1 < len(s) && s[i+1] == '\'':
			buf.WriteByte('\'')
			buf.WriteByte('\'')
			i++
		case c == '\'':
			inString = !inString
			buf.WriteByte(c)
		case c == ',' && !inString:
			out = append(out, buf.String())
			buf.Reset()
		default:
			buf.WriteByte(c)
		}
	}
	if inString {
		return nil, fmt.Errorf("unterminated string literal")
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out, nil
}

// sqlUnquote strips the outer single quotes from a SQL string literal and
// replaces ” escape sequences with a single '.
func sqlUnquote(tok string) (string, error) {
	if !strings.HasPrefix(tok, "'") || !strings.HasSuffix(tok, "'") {
		return "", fmt.Errorf("expected quoted string, got %q", tok)
	}
	inner := tok[1 : len(tok)-1]
	return strings.ReplaceAll(inner, "''", "'"), nil
}
