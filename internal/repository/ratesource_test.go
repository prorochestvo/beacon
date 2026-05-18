package repository

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/seilbekskindirov/monitor/internal/domain"
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
}

//func TestRateSourceRepository_UpdateRateSourceActive(t *testing.T) {
//	t.Parallel()
//
//	// TODO: rethink
//	newRepo := func(t *testing.T) (*RateSourceRepository, *domain.RateSource) {
//		t.Helper()
//		r, err := NewRateSourceRepository(stubSQLiteDB(t))
//		require.NoError(t, err)
//		src := &domain.RateSource{
//			Name:          "toggle-src-" + t.Name(),
//			Title:         "Toggle Source",
//			URL:           "https://example.com/toggle",
//			Interval:      "10m",
//			BaseCurrency:  "USD",
//			QuoteCurrency: "KZT",
//			Rules:         []domain.RateSourceRule{},
//		}
//		require.NoError(t, r.RetainRateSource(t.Context(), src))
//		return r, src
//	}
//
//	t.Run("sets active to true", func(t *testing.T) {
//		t.Parallel()
//		r, src := newRepo(t)
//
//		require.NoError(t, r.UpdateRateSourceActive(t.Context(), src.Name, true))
//
//		result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
//		require.NoError(t, err)
//		require.NotNil(t, result)
//		require.True(t, result.Active)
//	})
//	t.Run("sets active to false", func(t *testing.T) {
//		t.Parallel()
//		r, src := newRepo(t)
//
//		require.NoError(t, r.UpdateRateSourceActive(t.Context(), src.Name, true))
//		require.NoError(t, r.UpdateRateSourceActive(t.Context(), src.Name, false))
//
//		result, err := r.ObtainRateSourceByName(t.Context(), src.Name)
//		require.NoError(t, err)
//		require.NotNil(t, result)
//		require.False(t, result.Active)
//	})
//	t.Run("returns ErrNotFound for unknown source", func(t *testing.T) {
//		t.Parallel()
//		r, err := NewRateSourceRepository(stubSQLiteDB(t))
//		require.NoError(t, err)
//
//		err = r.UpdateRateSourceActive(t.Context(), "no-such-source", true)
//		require.ErrorIs(t, err, internal.ErrNotFound)
//	})
//}

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
