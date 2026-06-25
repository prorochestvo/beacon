package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/seilbekskindirov/monitor/internal/application/sourceaudit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ sourceaudit.Fetcher = (*stubFetcher)(nil)

// stubFetcher is a sourceaudit.Fetcher test double returning pre-configured
// responses per URL without hitting the network.
type stubFetcher struct {
	responses map[string]*sourceaudit.FetchResult
	errs      map[string]error
}

func (s *stubFetcher) Fetch(_ context.Context, url string) (*sourceaudit.FetchResult, error) {
	if err, ok := s.errs[url]; ok {
		return nil, err
	}
	if r, ok := s.responses[url]; ok {
		return r, nil
	}
	return &sourceaudit.FetchResult{Body: []byte(""), StatusCode: 200}, nil
}

func TestRunAudit(t *testing.T) {
	t.Parallel()

	t.Run("no flags returns exit 2", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit(nil, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "specify --all, --source, or --only")
		assert.Empty(t, out.String())
	})

	t.Run("--all and --source are mutually exclusive", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--all", "--source", "halyk_usd"}, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "mutually exclusive")
	})

	t.Run("--all and --only are mutually exclusive", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--all", "--only", "^halyk"}, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "mutually exclusive")
	})

	t.Run("--source and --only are mutually exclusive", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--source", "halyk_usd", "--only", "^halyk"}, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "mutually exclusive")
	})

	t.Run("invalid --only regex returns exit 2", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--only", "[invalid"}, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "compile --only regex")
	})

	t.Run("missing seed glob returns exit 3", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		var out, errOut bytes.Buffer
		// Point seed-glob at an empty temp dir so ParseSeedFiles finds nothing.
		code := runAudit([]string{"--all", "--seed-glob", filepath.Join(dir, "*.sql")}, &out, &errOut)

		assert.Equal(t, 3, code)
		assert.Contains(t, errOut.String(), "no sources found")
	})

	t.Run("--help exits 0", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--help"}, &out, &errOut)

		assert.Equal(t, 0, code)
	})

	t.Run("--source with dots matches only exact name not regex-expanded", func(t *testing.T) {
		t.Parallel()

		// Validate the --source QuoteMeta contract directly: runAudit uses
		// os.DirFS(".") which would need a real repo root.
		source := "src.with.dots"
		pattern := `^` + regexp.QuoteMeta(source) + `$`
		re := regexp.MustCompile(pattern)

		assert.True(t, re.MatchString("src.with.dots"), "should match the exact name")
		assert.False(t, re.MatchString("srcXwithXdots"), "dot must be literal, not match any char")
		assert.False(t, re.MatchString("src.with.dotsSUFFIX"), "must be anchored")
		assert.False(t, re.MatchString("PREFIXsrc.with.dots"), "must be anchored at start")
	})

	t.Run("audit all sources via stubbed fetcher", func(t *testing.T) {
		t.Parallel()

		// Run the full audit pipeline on a minimal temp seed file to verify
		// exit-code semantics.
		dir := t.TempDir()
		seedPath := filepath.Join(dir, "seed.sql")
		rules := `[{"method":"regex","pattern":"([0-9]+\\.?[0-9]*)","target":""}]`
		row := fmt.Sprintf(
			`INSERT OR IGNORE INTO rate_sources VALUES('test_src','Vendor','USD','KZT','https://example.com/','1h','BID',1,'{}','%s','{}','plain');`,
			rules,
		)
		require.NoError(t, os.WriteFile(seedPath, []byte(row+"\n"), 0o644))

		sources, err := sourceaudit.ParseSeedFiles(os.DirFS(dir), "*.sql")
		require.NoError(t, err)
		require.Len(t, sources, 1)

		fetcher := &stubFetcher{
			responses: map[string]*sourceaudit.FetchResult{
				"https://example.com/": {Body: []byte("450.00"), StatusCode: 200},
			},
		}
		auditor := &sourceaudit.Auditor{Fetcher: fetcher}
		results, err := auditor.Run(t.Context(), sources)
		require.NoError(t, err)

		var out bytes.Buffer
		failures, err := sourceaudit.WriteReport(&out, results, false)
		require.NoError(t, err)
		assert.Equal(t, 0, failures, "expected all sources OK, got: %s", out.String())
	})

	t.Run("infrastructure error when auditor fetcher fails", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		seedPath := filepath.Join(dir, "seed.sql")
		rules := `[{"method":"regex","pattern":"([0-9]+\\.?[0-9]*)","target":""}]`
		row := fmt.Sprintf(
			`INSERT OR IGNORE INTO rate_sources VALUES('test_src','Vendor','USD','KZT','https://example.com/','1h','BID',1,'{}','%s','{}','plain');`,
			rules,
		)
		require.NoError(t, os.WriteFile(seedPath, []byte(row+"\n"), 0o644))

		sources, err := sourceaudit.ParseSeedFiles(os.DirFS(dir), "*.sql")
		require.NoError(t, err)
		require.Len(t, sources, 1)

		// Auditor.Run only errors on context cancel; the FETCH_ERROR path instead
		// maps to failures > 0 → exit 1.
		fetcher := &stubFetcher{
			errs: map[string]error{
				"https://example.com/": fmt.Errorf("connection refused"),
			},
		}
		auditor := &sourceaudit.Auditor{Fetcher: fetcher}
		results, err := auditor.Run(t.Context(), sources)
		require.NoError(t, err) // Auditor.Run itself doesn't error on fetch failure

		var out bytes.Buffer
		failures, err := sourceaudit.WriteReport(&out, results, false)
		require.NoError(t, err)
		assert.Greater(t, failures, 0, "expected at least one MISS when fetch fails")
	})

	t.Run("fetch failure produces exit code 1 via runAuditWith", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		seedPath := filepath.Join(dir, "seed.sql")
		rules := `[{"method":"regex","pattern":"([0-9]+\\.?[0-9]*)","target":""}]`
		row := fmt.Sprintf(
			`INSERT OR IGNORE INTO rate_sources VALUES('miss_src','Vendor','USD','KZT','https://example.com/','1h','BID',1,'{}','%s','{}','plain');`,
			rules,
		)
		require.NoError(t, os.WriteFile(seedPath, []byte(row+"\n"), 0o644))
		_ = seedPath // written so os.DirFS(dir) can find it via the glob "*.sql"

		fetcher := &stubFetcher{
			errs: map[string]error{
				"https://example.com/": fmt.Errorf("connection refused"),
			},
		}

		var out, errOut bytes.Buffer
		code := runAuditWith(
			[]string{"--all", "--seed-glob", "*.sql"},
			fetcher,
			os.DirFS(dir),
			&out,
			&errOut,
		)

		assert.Equal(t, 1, code, "expected exit 1 when at least one source is a MISS; stdout: %s", out.String())
	})

	t.Run("--source with dots only matches exact name via runAuditWith", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		rules := `[{"method":"regex","pattern":"([0-9]+\\.?[0-9]*)","target":""}]`

		// Two sources: the exact target and a look-alike that would match if dots
		// were regex wildcards.
		row1 := fmt.Sprintf(
			`INSERT OR IGNORE INTO rate_sources VALUES('src.with.dots','Vendor','USD','KZT','https://exact.example.com/','1h','BID',1,'{}','%s','{}','plain');`,
			rules,
		)
		row2 := fmt.Sprintf(
			`INSERT OR IGNORE INTO rate_sources VALUES('srcXwithXdots','Vendor','USD','KZT','https://wildcard.example.com/','1h','BID',1,'{}','%s','{}','plain');`,
			rules,
		)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.sql"), []byte(row1+"\n"+row2+"\n"), 0o644))

		// Return OK for both sources so exit code reflects filter results only.
		fetcher := &stubFetcher{
			responses: map[string]*sourceaudit.FetchResult{
				"https://exact.example.com/":    {Body: []byte("450.00"), StatusCode: 200},
				"https://wildcard.example.com/": {Body: []byte("300.00"), StatusCode: 200},
			},
		}

		var out, errOut bytes.Buffer
		// -v makes WriteReport emit the per-source table with source names; the
		// non-verbose OK line ("OK: audited N sources across N URLs") does not.
		code := runAuditWith(
			[]string{"--source", "src.with.dots", "--seed-glob", "*.sql", "-v"},
			fetcher,
			os.DirFS(dir),
			&out,
			&errOut,
		)

		// A regex-dot bug would match srcXwithXdots too (two sources). With
		// QuoteMeta only src.with.dots is audited; its 450.00 extracts → exit 0.
		assert.Equal(t, 0, code, "exact-name match should exit 0; stdout: %s stderr: %s", out.String(), errOut.String())
		assert.Contains(t, out.String(), "src.with.dots", "report must mention the audited source")
		assert.NotContains(t, out.String(), "srcXwithXdots", "wildcard source must not appear in report")
	})
}
