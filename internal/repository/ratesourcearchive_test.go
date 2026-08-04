package repository

import (
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archiveSource builds a source definition with every column populated, so a
// round-trip through the mirror proves the whole row survived rather than just the
// two columns the history join reads.
func archiveSource(name, title string) domain.RateSource {
	return domain.RateSource{
		Name:          name,
		Title:         title,
		BaseCurrency:  "USD",
		QuoteCurrency: "KZT",
		URL:           "https://example.test/" + name,
		Interval:      "10m",
		Kind:          domain.RateSourceKindBID,
		Active:        true,
		FetcherKind:   "plain",
	}
}

func TestRateSourceArchiveRepository_RetainRateSources(t *testing.T) {
	t.Parallel()

	t.Run("inserts new rows and reports the count", func(t *testing.T) {
		t.Parallel()
		db := stubArchiveDB(t)
		repo, err := NewRateSourceArchiveRepository(db)
		require.NoError(t, err)

		affected, err := repo.RetainRateSources(t.Context(), []domain.RateSource{
			archiveSource("src-a", "Provider A"),
			archiveSource("src-b", "Provider B"),
		})
		require.NoError(t, err)
		assert.Equal(t, 2, affected)

		reader, err := NewRateSourceRepository(db)
		require.NoError(t, err)
		stored, err := reader.ObtainAllRateSources(t.Context())
		require.NoError(t, err)
		require.Len(t, stored, 2)
	})

	t.Run("the mirror reads back through the ordinary source repository", func(t *testing.T) {
		t.Parallel()
		db := stubArchiveDB(t)
		repo, err := NewRateSourceArchiveRepository(db)
		require.NoError(t, err)

		want := archiveSource("src-a", "Provider A")
		want.Options = domain.RateSourceOptions{Headers: map[string]string{"User-Agent": "beacon"}}
		_, err = repo.RetainRateSources(t.Context(), []domain.RateSource{want})
		require.NoError(t, err)

		// This is the property the mirror exists for: the archive carries the hot
		// schema's columns, so hot-tier repository code reads it with no second
		// implementation to keep in step.
		reader, err := NewRateSourceRepository(db)
		require.NoError(t, err)
		got, err := reader.ObtainRateSourceByName(t.Context(), "src-a")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, want.Title, got.Title)
		assert.Equal(t, want.BaseCurrency, got.BaseCurrency)
		assert.Equal(t, want.QuoteCurrency, got.QuoteCurrency)
		assert.Equal(t, want.URL, got.URL)
		assert.Equal(t, want.Kind, got.Kind)
		assert.Equal(t, want.Active, got.Active)
		assert.Equal(t, want.FetcherKind, got.FetcherKind)
		assert.Equal(t, "beacon", got.Options.Headers["User-Agent"])
	})

	t.Run("a repeated name overwrites rather than duplicating", func(t *testing.T) {
		t.Parallel()
		db := stubArchiveDB(t)
		repo, err := NewRateSourceArchiveRepository(db)
		require.NoError(t, err)

		_, err = repo.RetainRateSources(t.Context(), []domain.RateSource{archiveSource("src-a", "Old Title")})
		require.NoError(t, err)
		_, err = repo.RetainRateSources(t.Context(), []domain.RateSource{archiveSource("src-a", "New Title")})
		require.NoError(t, err)

		reader, err := NewRateSourceRepository(db)
		require.NoError(t, err)
		stored, err := reader.ObtainAllRateSources(t.Context())
		require.NoError(t, err)
		require.Len(t, stored, 1)
		assert.Equal(t, "New Title", stored[0].Title)
	})

	t.Run("a source dropped from the hot tier stays mirrored", func(t *testing.T) {
		t.Parallel()
		db := stubArchiveDB(t)
		repo, err := NewRateSourceArchiveRepository(db)
		require.NoError(t, err)

		_, err = repo.RetainRateSources(t.Context(), []domain.RateSource{
			archiveSource("src-a", "Provider A"),
			archiveSource("src-b", "Provider B"),
		})
		require.NoError(t, err)

		// A later pass sees only src-a, because src-b was deleted upstream. Its
		// archived values still need a title, so the mirror must keep the row.
		_, err = repo.RetainRateSources(t.Context(), []domain.RateSource{archiveSource("src-a", "Provider A")})
		require.NoError(t, err)

		reader, err := NewRateSourceRepository(db)
		require.NoError(t, err)
		stored, err := reader.ObtainAllRateSources(t.Context())
		require.NoError(t, err)
		assert.Len(t, stored, 2, "the mirror is append-and-update only")
	})

	t.Run("an empty slice is a no-op", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateSourceArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)

		affected, err := repo.RetainRateSources(t.Context(), nil)
		require.NoError(t, err)
		assert.Zero(t, affected)
	})

	t.Run("an empty name is rejected before any row is written", func(t *testing.T) {
		t.Parallel()
		db := stubArchiveDB(t)
		repo, err := NewRateSourceArchiveRepository(db)
		require.NoError(t, err)

		_, err = repo.RetainRateSources(t.Context(), []domain.RateSource{
			archiveSource("src-a", "Provider A"),
			archiveSource("", "Nameless"),
		})
		require.Error(t, err)

		reader, err := NewRateSourceRepository(db)
		require.NoError(t, err)
		stored, err := reader.ObtainAllRateSources(t.Context())
		require.NoError(t, err)
		assert.Empty(t, stored, "the batch is validated before the transaction opens")
	})

	t.Run("CheckUP reads the mirror", func(t *testing.T) {
		t.Parallel()
		repo, err := NewRateSourceArchiveRepository(stubArchiveDB(t))
		require.NoError(t, err)
		require.NoError(t, repo.CheckUP(t.Context()))
		assert.Equal(t, "rate_sources_archive", repo.Name())
	})
}
