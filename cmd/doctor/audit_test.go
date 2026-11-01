package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/prorochestvo/dsninjector"
	"github.com/seilbekskindirov/beacon/internal/application/sourceaudit"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb/sqlitedbtest"
	"github.com/seilbekskindirov/beacon/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ sourceaudit.Fetcher = (*stubFetcher)(nil)

// stubFetcher is a sourceaudit.Fetcher test double returning pre-configured
// responses per URL without hitting the network.
type stubFetcher struct {
	mu        sync.Mutex
	responses map[string]*sourceaudit.FetchResult
	errs      map[string]error
	requested []string
}

func (s *stubFetcher) Fetch(_ context.Context, url string, _ map[string]string) (*sourceaudit.FetchResult, error) {
	s.mu.Lock()
	s.requested = append(s.requested, url)
	s.mu.Unlock()
	if err, ok := s.errs[url]; ok {
		return nil, err
	}
	if r, ok := s.responses[url]; ok {
		return r, nil
	}
	return &sourceaudit.FetchResult{Body: []byte(""), StatusCode: 200}, nil
}

func (s *stubFetcher) requestedURLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requested...)
}

// warmSQLite opens one database file before any parallel subtest does. The
// modernc.org/sqlite driver initialises its global state on first open, and
// concurrent first opens trip the race detector.
var warmSQLite sync.Once

func warmUpSQLite(t *testing.T) {
	t.Helper()
	warmSQLite.Do(func() {
		ds, err := dsninjector.Parse("sqlite://_:_@_:_/" + filepath.Join(t.TempDir(), "warm.db"))
		require.NoError(t, err)
		db, err := sqlitedb.NewSQLiteClient(ds)
		require.NoError(t, err)
		require.NoError(t, db.Close())
	})
}

func newMigratedAuditDB(t *testing.T) string {
	t.Helper()
	warmUpSQLite(t)
	dsn := "sqlite://_:_@_:_/" + filepath.Join(t.TempDir(), "audit.db")
	ds, err := dsninjector.Parse(dsn)
	require.NoError(t, err)
	db, err := sqlitedb.NewSQLiteClient(ds)
	require.NoError(t, err)
	sqlitedbtest.Apply(t, db)
	require.NoError(t, db.Close())
	return dsn
}

func openAuditDB(t *testing.T, dsn string) *sqlitedb.SQLiteClient {
	t.Helper()
	warmUpSQLite(t)
	ds, err := dsninjector.Parse(dsn)
	require.NoError(t, err)
	db, err := sqlitedb.NewSQLiteClient(ds)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestRunAudit(t *testing.T) {
	t.Parallel()

	t.Run("no flags returns exit 2", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit(nil, nil, nil, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "specify --all, --source, or --only")
		assert.Empty(t, out.String())
	})

	t.Run("--all and --source are mutually exclusive", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--all", "--source", "halyk_usd"}, nil, nil, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "mutually exclusive")
	})

	t.Run("--all and --only are mutually exclusive", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--all", "--only", "^halyk"}, nil, nil, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "mutually exclusive")
	})

	t.Run("--source and --only are mutually exclusive", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--source", "halyk_usd", "--only", "^halyk"}, nil, nil, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "mutually exclusive")
	})

	t.Run("invalid --only regex returns exit 2", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--only", "[invalid"}, nil, nil, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "compile --only regex")
	})

	t.Run("missing seed glob returns exit 3", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		var out, errOut bytes.Buffer
		// Point seed-glob at an empty temp dir so ParseSeedFiles finds nothing.
		code := runAudit([]string{"--all", "--seed-glob", filepath.Join(dir, "*.sql")}, nil, nil, &out, &errOut)

		assert.Equal(t, 3, code)
		assert.Contains(t, errOut.String(), "no sources found")
	})

	t.Run("--help exits 0", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := runAudit([]string{"--help"}, nil, nil, &out, &errOut)

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
				"https://example.com/": errors.New("connection refused"),
			},
		}
		auditor := &sourceaudit.Auditor{Fetcher: fetcher}
		results, err := auditor.Run(t.Context(), sources)
		require.NoError(t, err) // Auditor.Run itself doesn't error on fetch failure

		var out bytes.Buffer
		failures, err := sourceaudit.WriteReport(&out, results, false)
		require.NoError(t, err)
		assert.Positive(t, failures, "expected at least one MISS when fetch fails")
	})

	t.Run("fetch failure produces exit code 1 via runAudit", func(t *testing.T) {
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
				"https://example.com/": errors.New("connection refused"),
			},
		}

		var out, errOut bytes.Buffer
		code := runAudit(
			[]string{"--all", "--seed-glob", "*.sql"},
			fetcher,
			os.DirFS(dir),
			&out,
			&errOut,
		)

		assert.Equal(t, 1, code, "expected exit 1 when at least one source is a MISS; stdout: %s", out.String())
	})

	t.Run("--source with dots only matches exact name via runAudit", func(t *testing.T) {
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
		code := runAudit(
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

func TestRunAuditDatabaseMode(t *testing.T) {
	t.Parallel()

	const bccURL = "https://www.bcc.kz/en/personal/currency-rates/"
	const bccBody = `currencyMain&quot;:&quot;USD&quot;,&quot;sell&quot;:478.5`

	t.Run("--dsn and explicitly set --seed-glob are incompatible", func(t *testing.T) {
		t.Parallel()
		dsn := newMigratedAuditDB(t)
		var out, errOut bytes.Buffer
		code := runAudit([]string{"--dsn", dsn, "--seed-glob", "migrations/*.sql", "--all"}, nil, nil, &out, &errOut)
		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "--dsn and --seed-glob")
		assert.Empty(t, out.String())
	})

	t.Run("invalid DSN is a usage error", func(t *testing.T) {
		t.Parallel()
		var out, errOut bytes.Buffer
		// dsninjector.Parse rejects only input its pattern cannot match, such as a newline in the query part.
		code := runAudit([]string{"--dsn", "sqlite://_:_@_:_/audit.db?a=1\nb=2", "--all"}, nil, nil, &out, &errOut)
		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "parse --dsn")
		assert.Empty(t, out.String())
	})

	t.Run("missing database file is not created", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "missing.db")
		dsn := "sqlite://_:_@_:_/" + path
		var out, errOut bytes.Buffer
		code := runAudit([]string{"--dsn", dsn, "--all"}, nil, nil, &out, &errOut)
		assert.Equal(t, 3, code)
		assert.Contains(t, errOut.String(), "database file not found")
		assert.Empty(t, out.String())
		_, err := os.Stat(path)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("empty unmigrated database is rejected", func(t *testing.T) {
		t.Parallel()
		dsn := "sqlite://_:_@_:_/" + filepath.Join(t.TempDir(), "audit.db")
		db := openAuditDB(t, dsn)
		require.NoError(t, db.Close())
		var out, errOut bytes.Buffer
		code := runAudit([]string{"--dsn", dsn, "--all"}, nil, nil, &out, &errOut)
		assert.Equal(t, 3, code)
		assert.Contains(t, errOut.String(), "schema behind this build: run cmd/migrator")
		assert.Empty(t, out.String())
	})

	t.Run("database mode reads effective BCC rules while seed mode reads INSERT rules", func(t *testing.T) {
		t.Parallel()
		dsn := newMigratedAuditDB(t)
		fetcher := &stubFetcher{responses: map[string]*sourceaudit.FetchResult{
			bccURL: {Body: []byte(bccBody), StatusCode: 200},
		}}
		var dbOut, dbErr bytes.Buffer
		code := runAudit([]string{"--dsn", dsn, "--source", "KZ_BCC_FX_BID_USD_KZT"}, fetcher, nil, &dbOut, &dbErr)
		assert.Equal(t, 0, code, "stdout: %s stderr: %s", dbOut.String(), dbErr.String())
		assert.Contains(t, dbOut.String(), "OK")

		seedFetcher := &stubFetcher{responses: map[string]*sourceaudit.FetchResult{
			bccURL: {Body: []byte(bccBody), StatusCode: 200},
		}}
		var seedOut, seedErr bytes.Buffer
		code = runAudit([]string{"--source", "KZ_BCC_FX_BID_USD_KZT"}, seedFetcher, os.DirFS("../.."), &seedOut, &seedErr)
		assert.Equal(t, 1, code, "stdout: %s stderr: %s", seedOut.String(), seedErr.String())
		assert.Contains(t, seedOut.String(), "REGEX_NO_MATCH")
	})

	t.Run("--all omits source deleted by migration 008", func(t *testing.T) {
		t.Parallel()
		dsn := newMigratedAuditDB(t)
		var out, errOut bytes.Buffer
		code := runAudit([]string{"--dsn", dsn, "--all", "-v"}, &stubFetcher{}, nil, &out, &errOut)
		assert.NotEqual(t, 2, code)
		assert.NotContains(t, out.String(), "KZ_BCC_BID_USD_KZT")
		assert.Contains(t, errOut.String(), "active sources from rate_sources")
	})

	t.Run("inactive rows are excluded and chromedp rows are skipped", func(t *testing.T) {
		t.Parallel()
		dsn := newMigratedAuditDB(t)
		db := openAuditDB(t, dsn)
		repo, err := repository.NewRateSourceRepository(db)
		require.NoError(t, err)
		require.NoError(t, repo.RetainRateSource(t.Context(), &domain.RateSource{
			Name: "audit-inactive", Title: "Inactive", BaseCurrency: "USD", QuoteCurrency: "KZT",
			URL: "https://inactive.example/", Interval: "1h", Kind: domain.RateSourceKindBID, Active: false,
			FetcherKind: "plain", Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: "([0-9]+)"}},
		}))
		require.NoError(t, repo.RetainRateSource(t.Context(), &domain.RateSource{
			Name: "audit-chromedp", Title: "Chromedp", BaseCurrency: "USD", QuoteCurrency: "KZT",
			URL: "https://chromedp.example/", Interval: "1h", Kind: domain.RateSourceKindBID, Active: true,
			FetcherKind: "chromedp", Rules: []domain.RateSourceRule{{Method: domain.MethodRegex, Pattern: "([0-9]+)"}},
		}))
		require.NoError(t, db.Close())

		fetcher := &stubFetcher{}
		var out, errOut bytes.Buffer
		code := runAudit([]string{"--dsn", dsn, "--source", "audit-inactive"}, fetcher, nil, &out, &errOut)
		assert.Equal(t, 3, code)
		assert.Contains(t, errOut.String(), "no sources matched")
		assert.Empty(t, fetcher.requestedURLs())

		out.Reset()
		errOut.Reset()
		code = runAudit([]string{"--dsn", dsn, "--source", "audit-chromedp"}, fetcher, nil, &out, &errOut)
		assert.Equal(t, 3, code)
		assert.Contains(t, errOut.String(), "skipped audit-chromedp: fetcher_kind=chromedp is not audited (plain HTTP only)")
		assert.Contains(t, errOut.String(), "active sources from rate_sources")
		assert.Empty(t, fetcher.requestedURLs())
		assert.Empty(t, out.String())
	})
}
