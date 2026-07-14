package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb/sqlitedbtest"
	"github.com/stretchr/testify/require"
)

func TestNewSourceRepository(t *testing.T) {
	t.Parallel()

	r, err := NewRateSourceRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestSourceRepository_Name(t *testing.T) {
	t.Parallel()

	r, err := NewRateSourceRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.Equal(t, rateSourceTableName, r.Name())
}

func TestSourceRepository_CheckUP(t *testing.T) {
	t.Parallel()

	r, err := NewRateSourceRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NoError(t, r.CheckUP(t.Context()))
}

func TestSourceRepository_RetainSource(t *testing.T) {
	t.Parallel()

	r, err := NewRateSourceRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()

		err := r.RetainRateSource(t.Context(), nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "nil")
	})
	t.Run("insert", func(t *testing.T) {
		t.Parallel()

		src := &domain.RateSource{
			Name:          "src-insert",
			Title:         "src-insert-title",
			URL:           "https://example.com/insert",
			Interval:      "5m",
			BaseCurrency:  "USD",
			QuoteCurrency: "EUR",
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `(\d+\.\d+)`, Options: ""},
			},
		}
		require.NoError(t, r.RetainRateSource(t.Context(), src))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM"+" "+rateSourceTableName+
				" WHERE "+rateSourceNameFieldName+" = ?",
			src.Name,
		).Scan(&count))
		require.Equal(t, 1, count)
	})
	t.Run("update", func(t *testing.T) {
		t.Parallel()

		src := &domain.RateSource{
			Name:          "src-update",
			Title:         "src-update-title",
			URL:           "https://example.com/update/v1",
			Interval:      "10m",
			BaseCurrency:  "USD",
			QuoteCurrency: "EUR",
			Rules:         []domain.RateSourceRule{},
		}
		require.NoError(t, r.RetainRateSource(t.Context(), src))

		src.URL = "https://example.com/update/v2"
		src.Interval = "1h"
		require.NoError(t, r.RetainRateSource(t.Context(), src))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var url, interval string
		require.NoError(t, tx.QueryRow(
			"SELECT "+rateSourceURLFieldName+", "+reteSourceIntervalFieldName+
				" FROM "+rateSourceTableName+" WHERE "+rateSourceNameFieldName+" = ?",
			src.Name,
		).Scan(&url, &interval))
		require.Equal(t, "https://example.com/update/v2", url)
		require.Equal(t, "1h", interval)
	})
	t.Run("persists rule_metadata round-trip", func(t *testing.T) {
		t.Parallel()

		src := &domain.RateSource{
			Name:          "src-rule-metadata",
			Title:         "Rule Metadata Source",
			URL:           "https://example.com/rule-metadata",
			Interval:      "5m",
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Kind:          domain.RateSourceKindASK,
			Active:        true,
			Rules:         []domain.RateSourceRule{},
			RuleMetadata: domain.RateSourceRuleMetadata{
				Provider:     "groq",
				Model:        "llama-3.1-8b-instant",
				AttemptsUsed: 2,
				GeneratedAt:  "2026-05-14T10:00:00Z",
			},
		}
		require.NoError(t, r.RetainRateSource(t.Context(), src))

		result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, "groq", result.RuleMetadata.Provider)
		require.Equal(t, "llama-3.1-8b-instant", result.RuleMetadata.Model)
		require.Equal(t, 2, result.RuleMetadata.AttemptsUsed)
		require.Equal(t, "2026-05-14T10:00:00Z", result.RuleMetadata.GeneratedAt)
	})
	t.Run("sets active to true", func(t *testing.T) {
		t.Parallel()

		src := &domain.RateSource{
			Name:          "toggle-src-" + t.Name(),
			Title:         "Toggle Source",
			URL:           "https://example.com/toggle",
			Interval:      "10m",
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Kind:          domain.RateSourceKindASK,
			Active:        false,
			Rules:         []domain.RateSourceRule{},
		}
		require.NoError(t, r.RetainRateSource(t.Context(), src))
		result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.False(t, result.Active)

		src.Active = true
		require.NoError(t, r.RetainRateSource(t.Context(), src))

		result, err = r.ObtainRateSourceByName(t.Context(), src.Name)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.True(t, result.Active)
	})

	t.Run("persists Options.Headers round-trip", func(t *testing.T) {
		t.Parallel()

		t.Run("non-empty Headers read back equal", func(t *testing.T) {
			t.Parallel()

			src := &domain.RateSource{
				Name:          "opts-headers-" + t.Name(),
				Title:         "Options Headers Source",
				URL:           "https://example.com/opts-headers",
				Interval:      "5m",
				BaseCurrency:  "AAPL",
				QuoteCurrency: "USD",
				Kind:          domain.RateSourceKindLAST,
				Active:        true,
				Options: domain.RateSourceOptions{
					Headers: map[string]string{"User-Agent": "Bot/1.0"},
				},
				Rules: []domain.RateSourceRule{},
			}
			require.NoError(t, r.RetainRateSource(t.Context(), src))

			result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, map[string]string{"User-Agent": "Bot/1.0"}, result.Options.Headers,
				"Options.Headers must survive a round-trip through the database")
		})

		t.Run("empty options reads back nil Headers", func(t *testing.T) {
			t.Parallel()

			src := &domain.RateSource{
				Name:          "opts-headers-empty-" + t.Name(),
				Title:         "Options Headers Empty",
				URL:           "https://example.com/opts-headers-empty",
				Interval:      "5m",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Kind:          domain.RateSourceKindBID,
				Active:        true,
				Options:       domain.RateSourceOptions{}, // no Headers
				Rules:         []domain.RateSourceRule{},
			}
			require.NoError(t, r.RetainRateSource(t.Context(), src))

			result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Nil(t, result.Options.Headers,
				"Options.Headers must be nil when no headers were stored (omitempty)")
		})
	})

	t.Run("persists fetcher_kind round-trip", func(t *testing.T) {
		t.Parallel()

		t.Run("plain value is stored and retrieved", func(t *testing.T) {
			t.Parallel()

			src := &domain.RateSource{
				Name:          "fk-plain-" + t.Name(),
				Title:         "FetcherKind Plain",
				URL:           "https://example.com/fk-plain",
				Interval:      "5m",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Kind:          domain.RateSourceKindASK,
				Active:        true,
				FetcherKind:   "plain",
				Rules:         []domain.RateSourceRule{},
			}
			require.NoError(t, r.RetainRateSource(t.Context(), src))

			result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "plain", result.FetcherKind)
		})

		t.Run("chromedp value is stored and retrieved", func(t *testing.T) {
			t.Parallel()

			src := &domain.RateSource{
				Name:          "fk-chromedp-" + t.Name(),
				Title:         "FetcherKind Chromedp",
				URL:           "https://example.com/fk-chromedp",
				Interval:      "5m",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Kind:          domain.RateSourceKindBID,
				Active:        true,
				FetcherKind:   "chromedp",
				Rules:         []domain.RateSourceRule{},
			}
			require.NoError(t, r.RetainRateSource(t.Context(), src))

			result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "chromedp", result.FetcherKind)
		})

		t.Run("empty string is normalised to plain", func(t *testing.T) {
			t.Parallel()

			src := &domain.RateSource{
				Name:          "fk-empty-" + t.Name(),
				Title:         "FetcherKind Empty",
				URL:           "https://example.com/fk-empty",
				Interval:      "5m",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Kind:          domain.RateSourceKindASK,
				Active:        true,
				FetcherKind:   "", // legacy: should be treated as plain
				Rules:         []domain.RateSourceRule{},
			}
			require.NoError(t, r.RetainRateSource(t.Context(), src))

			result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "plain", result.FetcherKind)
		})

		t.Run("unsupported value returns error", func(t *testing.T) {
			t.Parallel()

			src := &domain.RateSource{
				Name:          "fk-bad-playwright-" + t.Name(),
				Title:         "FetcherKind Bad Playwright",
				URL:           "https://example.com/fk-bad",
				Interval:      "5m",
				BaseCurrency:  "USD",
				QuoteCurrency: "KZT",
				Kind:          domain.RateSourceKindASK,
				Active:        true,
				FetcherKind:   "playwright", // genuinely unknown; not in the allowed set
				Rules:         []domain.RateSourceRule{},
			}
			err := r.RetainRateSource(t.Context(), src)
			require.Error(t, err)
			require.Contains(t, err.Error(), "unsupported fetcher_kind")
		})
	})
}

func TestSourceRepository_RemoveSource(t *testing.T) {
	t.Parallel()

	r, err := NewRateSourceRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()

		err := r.RemoveRateSource(t.Context(), nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "nil")
	})
	t.Run("delete", func(t *testing.T) {
		t.Parallel()

		src := &domain.RateSource{
			Name:          "src-delete",
			Title:         "src-delete-title",
			URL:           "https://example.com/delete",
			Interval:      "10m",
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Rules:         []domain.RateSourceRule{},
		}

		require.NoError(t, r.RetainRateSource(t.Context(), src))
		require.NoError(t, r.RemoveRateSource(t.Context(), src))

		tx, err := r.db.Transaction(t.Context())
		require.NoError(t, err)
		defer func(tx *sql.Tx) { require.NoError(t, tx.Rollback()) }(tx)

		var count int
		require.NoError(t, tx.QueryRow(
			"SELECT COUNT(*) FROM"+" "+rateSourceTableName+
				" WHERE "+rateSourceNameFieldName+" = ?",
			src.Name,
		).Scan(&count))
		require.Equal(t, 0, count)
	})
	t.Run("cascade removes dependent rows", func(t *testing.T) {
		t.Parallel()

		// Dedicated DB with pool > 1 so FK enforcement runs against the
		// production-like wiring. The shared stubSQLiteDB helper
		// uses SetMaxOpenConns(1), which would hide a per-connection PRAGMA
		// regression behind a single connection that always inherits the
		// db.Exec defaults in NewSQLiteClientEx.
		db := newCascadeDB(t)

		// Pin the first pool slot in a parked transaction so the cascade
		// workload below opens fresh connections from later slots. Without
		// per-connection PRAGMA wiring those fresh slots would have
		// foreign_keys=0 and the DELETE on rate_sources would leave child
		// rows behind, failing the post-condition assertions.
		park, err := db.Transaction(t.Context())
		require.NoError(t, err)
		t.Cleanup(func() { _ = park.Rollback() })

		rateSources, err := NewRateSourceRepository(db)
		require.NoError(t, err)
		rateValues, err := NewRateValueRepository(db)
		require.NoError(t, err)
		rateSubs, err := NewRateUserSubscriptionRepository(db)
		require.NoError(t, err)
		rateEvents, err := NewRateUserEventRepository(db)
		require.NoError(t, err)

		const srcName = "src-cascade"
		src := &domain.RateSource{
			Name:          srcName,
			Title:         "cascade-test",
			URL:           "https://example.com/cascade",
			Interval:      "10m",
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Kind:          "BID",
			Active:        true,
		}
		require.NoError(t, rateSources.RetainRateSource(t.Context(), src))

		require.NoError(t, rateValues.RetainRateValue(t.Context(), &domain.RateValue{
			SourceName:    srcName,
			BaseCurrency:  "USD",
			QuoteCurrency: "KZT",
			Price:         500.0,
		}))
		require.NoError(t, rateSubs.RetainRateUserSubscription(t.Context(), &domain.RateUserSubscription{
			UserType:       domain.UserTypeTelegram,
			UserID:         "user-cascade",
			SourceName:     srcName,
			ConditionType:  domain.ConditionTypeDelta,
			ConditionValue: "0.5",
		}))
		require.NoError(t, rateEvents.RetainRateUserEvent(t.Context(), &domain.RateUserEvent{
			UserType:   domain.UserTypeTelegram,
			UserID:     "user-cascade",
			SourceName: srcName,
			Message:    "cascade-test",
		}))

		// Sanity: child rows are present before removal so the post-condition
		// assertions are not vacuously satisfied.
		require.Equal(t, 1, countRowsBySource(t, db,
			rateValueTableName, rateValueSourceNameFieldName, srcName))
		require.Equal(t, 1, countRowsBySource(t, db,
			rateUserSubscriptionTableName, rateUserSubscriptionSourceNameFieldName, srcName))
		require.Equal(t, 1, countRowsBySource(t, db,
			rateUserEventTableName, rateUserEventSourceNameFieldName, srcName))

		require.NoError(t, rateSources.RemoveRateSource(t.Context(), src))

		// CASCADE contract: every child row keyed on the removed source
		// must be gone.
		require.Zero(t, countRowsBySource(t, db,
			rateValueTableName, rateValueSourceNameFieldName, srcName))
		require.Zero(t, countRowsBySource(t, db,
			rateUserSubscriptionTableName, rateUserSubscriptionSourceNameFieldName, srcName))
		require.Zero(t, countRowsBySource(t, db,
			rateUserEventTableName, rateUserEventSourceNameFieldName, srcName))
	})
}

// newCascadeDB opens an in-memory SQLite DB with SetMaxOpenConns(4) and the
// production PRAGMA wiring (foreign_keys=1, busy_timeout=5000) supplied via
// DSN parameters so every pool connection inherits them. Used by the
// cascade subtest to exercise the same FK enforcement path as production.
func newCascadeDB(t *testing.T) *sqlitedb.SQLiteClient {
	t.Helper()

	safeName := strings.ReplaceAll(t.Name(), "/", "_")
	dsn := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		safeName,
	)
	mem, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	mem.SetMaxOpenConns(4)

	db, err := sqlitedb.NewSQLiteClientEx(mem, os.Stdout)
	require.NoError(t, err)
	sqlitedbtest.Apply(t, db)
	return db
}

// countRowsBySource counts rows in table where field equals name.
func countRowsBySource(t *testing.T, db *sqlitedb.SQLiteClient, table, field, name string) int {
	t.Helper()
	tx, err := db.Transaction(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var n int
	require.NoError(t, tx.QueryRow(
		"SELECT COUNT(*) FROM"+" "+table+" WHERE "+field+" = ?",
		name,
	).Scan(&n))
	return n
}

func TestSourceRepository_ObtainSourceByName(t *testing.T) {
	t.Parallel()

	r, err := NewRateSourceRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("found", func(t *testing.T) {
		t.Parallel()

		src := &domain.RateSource{
			Name:          "src-find",
			Title:         "src-find-title",
			URL:           "https://example.com/find",
			Interval:      "15m",
			BaseCurrency:  "USD",
			QuoteCurrency: "GBP",
			Rules: []domain.RateSourceRule{
				{Method: domain.MethodRegex, Pattern: `\d+`, Options: "i"},
			},
		}
		require.NoError(t, r.RetainRateSource(t.Context(), src))

		result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, src.Name, result.Name)
		require.Equal(t, src.Title, result.Title)
		require.Equal(t, src.URL, result.URL)
		require.Equal(t, src.Interval, result.Interval)
		require.Equal(t, src.BaseCurrency, result.BaseCurrency)
		require.Equal(t, src.QuoteCurrency, result.QuoteCurrency)
		require.Len(t, result.Rules, 1)
		require.Equal(t, src.Rules[0].Method, result.Rules[0].Method)
		require.Equal(t, src.Rules[0].Pattern, result.Rules[0].Pattern)
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()

		result, err := r.ObtainRateSourceByName(t.Context(), "nonexistent")
		require.NoError(t, err)
		require.Nil(t, result)
	})

	t.Run("LAST kind", func(t *testing.T) {
		t.Parallel()

		src := &domain.RateSource{
			Name:          "src-last-single",
			Title:         "LAST Single Lookup",
			URL:           "https://example.com/last-single",
			Interval:      "6h",
			BaseCurrency:  "AAPL",
			QuoteCurrency: "USD",
			Kind:          domain.RateSourceKindLAST,
			Active:        true,
			Rules:         []domain.RateSourceRule{},
		}
		require.NoError(t, r.RetainRateSource(t.Context(), src))

		result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, domain.RateSourceKindLAST, result.Kind,
			"LAST kind must survive ObtainRateSourceByName round-trip without error")
	})
}

func TestSourceRepository_ObtainAllSources(t *testing.T) {
	t.Parallel()

	r, err := NewRateSourceRepository(stubSQLiteDB(t))
	require.NoError(t, err)
	require.NotNil(t, r)

	t.Run("returns sources", func(t *testing.T) {
		t.Parallel()

		sources := []domain.RateSource{
			{Name: "src-all-1", URL: "https://example.com/1", Interval: "5m", BaseCurrency: "USD", QuoteCurrency: "EUR", Kind: domain.RateSourceKindBID, Rules: []domain.RateSourceRule{}},
			{Name: "src-all-2", URL: "https://example.com/2", Interval: "10m", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindASK, Rules: []domain.RateSourceRule{}},
		}
		for _, source := range sources {
			require.NoError(t, r.RetainRateSource(t.Context(), &source))
		}

		result, err := r.ObtainAllRateSources(t.Context())
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(result), 2)
	})
	t.Run("returns seeded sources without user inserts", func(t *testing.T) {
		t.Parallel()

		seededRepo, err := NewRateSourceRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		result, err := seededRepo.ObtainAllRateSources(t.Context())
		require.NoError(t, err)
		require.NotEmpty(t, result)
	})

	t.Run("returns LAST-kind source without unknown-kind error", func(t *testing.T) {
		t.Parallel()

		lastRepo, err := NewRateSourceRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		src := &domain.RateSource{
			Name:          "last-kind-roundtrip",
			Title:         "LAST Kind Round-trip",
			URL:           "https://example.com/last",
			Interval:      "6h",
			BaseCurrency:  "AAPL",
			QuoteCurrency: "USD",
			Kind:          domain.RateSourceKindLAST,
			Active:        true,
			Rules:         []domain.RateSourceRule{},
		}
		require.NoError(t, lastRepo.RetainRateSource(t.Context(), src))

		result, err := lastRepo.ObtainAllRateSources(t.Context())
		require.NoError(t, err)

		var found bool
		for _, s := range result {
			if s.Name == src.Name {
				require.Equal(t, domain.RateSourceKindLAST, s.Kind)
				found = true
			}
		}
		require.True(t, found, "LAST-kind source must appear in ObtainAllRateSources result")
	})

	t.Run("unknown kind XYZ is rejected by list query", func(t *testing.T) {
		t.Parallel()

		// Use a dedicated DB so the XYZ row does not contaminate other subtests.
		xyzRepo, err := NewRateSourceRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		// RetainRateSource does not validate kind, so the XYZ row persists fine.
		src := &domain.RateSource{
			Name:          "xyz-kind-rejection",
			Title:         "XYZ Kind Rejection",
			URL:           "https://example.com/xyz",
			Interval:      "6h",
			BaseCurrency:  "XYZ",
			QuoteCurrency: "USD",
			Kind:          "XYZ",
			Active:        true,
			Rules:         []domain.RateSourceRule{},
		}
		require.NoError(t, xyzRepo.RetainRateSource(t.Context(), src))

		_, err = xyzRepo.ObtainAllRateSources(t.Context())
		require.Error(t, err)
		require.ErrorContains(t, err, "unknown kind")
	})
}

func TestSourceRepository_ObtainRateSourcesByNames(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns empty map without querying", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateSourceRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		got, err := r.ObtainRateSourcesByNames(t.Context(), nil)
		require.NoError(t, err)
		require.Empty(t, got)
	})
	t.Run("returns requested sources, missing names absent from map", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateSourceRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		for _, src := range []domain.RateSource{
			{Name: "bulk-src-a", URL: "https://example.com/a", Interval: "5m", BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID, Title: "Bulk A"},
			{Name: "bulk-src-b", URL: "https://example.com/b", Interval: "5m", BaseCurrency: "EUR", QuoteCurrency: "KZT", Kind: domain.RateSourceKindASK, Title: "Bulk B"},
		} {
			require.NoError(t, r.RetainRateSource(t.Context(), &src))
		}

		got, err := r.ObtainRateSourcesByNames(t.Context(),
			[]string{"bulk-src-a", "bulk-src-b", "missing-source"})
		require.NoError(t, err)
		require.Len(t, got, 2, "missing-source must be absent")
		require.Equal(t, "Bulk A", got["bulk-src-a"].Title)
		require.Equal(t, "Bulk B", got["bulk-src-b"].Title)
	})
	t.Run("single-name list works (placeholder edge case)", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateSourceRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		require.NoError(t, r.RetainRateSource(t.Context(), &domain.RateSource{
			Name: "bulk-solo", URL: "https://example.com/solo", Interval: "5m",
			BaseCurrency: "USD", QuoteCurrency: "KZT", Kind: domain.RateSourceKindBID,
		}))

		got, err := r.ObtainRateSourcesByNames(t.Context(), []string{"bulk-solo"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Contains(t, got, "bulk-solo")
	})

	t.Run("LAST-kind source is returned without error", func(t *testing.T) {
		t.Parallel()

		r, err := NewRateSourceRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		require.NoError(t, r.RetainRateSource(t.Context(), &domain.RateSource{
			Name:          "bulk-last",
			Title:         "LAST Kind Bulk",
			URL:           "https://example.com/last-bulk",
			Interval:      "6h",
			BaseCurrency:  "AAPL",
			QuoteCurrency: "USD",
			Kind:          domain.RateSourceKindLAST,
			Active:        true,
			Rules:         []domain.RateSourceRule{},
		}))

		got, err := r.ObtainRateSourcesByNames(t.Context(), []string{"bulk-last"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, domain.RateSourceKindLAST, got["bulk-last"].Kind,
			"LAST kind must survive ObtainRateSourcesByNames round-trip without error")
	})
}

func TestSourceRepository_TransactionErrors(t *testing.T) {
	t.Parallel()

	newBrokenRepo := func(t *testing.T) *RateSourceRepository {
		t.Helper()
		r, err := NewRateSourceRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		r.db = &mockFailDB{err: errors.New("db unavailable")}
		return r
	}

	t.Run("CheckUP propagates transaction error", func(t *testing.T) {
		t.Parallel()
		require.Error(t, newBrokenRepo(t).CheckUP(t.Context()))
	})
	t.Run("ObtainRateSourceByName propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainRateSourceByName(t.Context(), "src")
		require.Error(t, err)
	})
	t.Run("ObtainAllRateSources propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainAllRateSources(t.Context())
		require.Error(t, err)
	})
	t.Run("ObtainRateSourcesByNames propagates transaction error", func(t *testing.T) {
		t.Parallel()
		_, err := newBrokenRepo(t).ObtainRateSourcesByNames(t.Context(), []string{"src"})
		require.Error(t, err)
	})
	t.Run("RetainRateSource propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RetainRateSource(t.Context(), &domain.RateSource{Name: "x"})
		require.Error(t, err)
	})
	t.Run("RemoveRateSource propagates transaction error", func(t *testing.T) {
		t.Parallel()
		err := newBrokenRepo(t).RemoveRateSource(t.Context(), &domain.RateSource{Name: "x"})
		require.Error(t, err)
	})
}
