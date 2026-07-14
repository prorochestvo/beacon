package repository

import (
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeatherGismeteoCityRepository_ObtainGismeteoCoverage(t *testing.T) {
	t.Parallel()

	t.Run("returns the four seeded cities keyed by location_id", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherGismeteoCityRepository(db)
		require.NoError(t, err)

		coverage, err := repo.ObtainGismeteoCoverage(t.Context())
		require.NoError(t, err)
		require.NotNil(t, coverage)
		require.Len(t, coverage, 4)

		// These four triples are the canonical gismeteo coverage: they must match
		// the former gismeteoCities Go literal (deleted in the infra refactor)
		// byte-for-byte. A drift here silently sends a city dark.
		want := map[string]domain.WeatherGismeteoCity{
			"1526384": {LocationID: "1526384", Slug: "almaty", GismeteoID: 5205, Label: "Almaty, Kazakhstan"},
			"1526273": {LocationID: "1526273", Slug: "astana", GismeteoID: 5164, Label: "Astana, Kazakhstan"},
			"1518980": {LocationID: "1518980", Slug: "shymkent", GismeteoID: 5324, Label: "Shymkent, Kazakhstan"},
			"524901":  {LocationID: "524901", Slug: "moscow", GismeteoID: 4368, Label: "Moscow, Russia"},
		}
		for locID, wantCity := range want {
			got, ok := coverage[locID]
			require.True(t, ok, "location_id %q must be in the coverage map", locID)
			assert.Equal(t, wantCity, got)
		}
	})

	t.Run("empty table returns a non-nil empty map", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)

		tx, err := db.Transaction(t.Context())
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), "DELETE FROM "+weatherGismeteoCityTableName+";")
		require.NoError(t, err)
		require.NoError(t, tx.Commit())

		repo, err := NewWeatherGismeteoCityRepository(db)
		require.NoError(t, err)

		coverage, err := repo.ObtainGismeteoCoverage(t.Context())
		require.NoError(t, err, "an empty coverage table is a valid state, not an error")
		require.NotNil(t, coverage, "coverage map must be non-nil even when empty")
		assert.Empty(t, coverage)
	})
}

func TestWeatherGismeteoCityRepository_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("succeeds against the seeded table", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherGismeteoCityRepository(db)
		require.NoError(t, err)

		require.NoError(t, repo.CheckUP(t.Context()))
	})

	t.Run("returns error when the DB is unavailable", func(t *testing.T) {
		t.Parallel()
		repo, err := NewWeatherGismeteoCityRepository(&mockFailDB{err: assert.AnError})
		require.NoError(t, err)

		require.Error(t, repo.CheckUP(t.Context()))
	})
}
