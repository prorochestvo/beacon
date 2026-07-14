package repository

import (
	"testing"

	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wantGismeteoUA pins the seeded gismeteo User-Agent. It must equal the
// gismeteoUserAgent constant in internal/infrastructure/weather byte-for-byte;
// the repository package must not import that package, so the literal is
// duplicated here on purpose to catch a drift in the migration seed.
const wantGismeteoUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

func TestWeatherSourceRepository_ObtainAllWeatherSources(t *testing.T) {
	t.Parallel()

	t.Run("returns the two seeded providers", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherSourceRepository(db)
		require.NoError(t, err)

		sources, err := repo.ObtainAllWeatherSources(t.Context())
		require.NoError(t, err)
		require.NotNil(t, sources)
		require.Len(t, sources, 2)

		// ObtainAllWeatherSources orders by provider ASC; "gismeteo" sorts before
		// "open-meteo".
		assert.Equal(t, domain.ProviderGismeteo, sources[0].Provider)
		assert.Equal(t, domain.ProviderOpenMeteo, sources[1].Provider)

		byProvider := make(map[string]domain.WeatherSource, len(sources))
		for _, s := range sources {
			byProvider[s.Provider] = s
		}

		openMeteo, ok := byProvider[domain.ProviderOpenMeteo]
		require.True(t, ok, "open-meteo row must be present")
		assert.Equal(t, "Open-Meteo", openMeteo.Title)
		assert.True(t, openMeteo.Active)
		assert.Empty(t, openMeteo.BaseURL, "open-meteo base_url is intentionally empty")
		assert.Empty(t, openMeteo.ThrottleInterval)
		assert.Empty(t, openMeteo.Options.UserAgent)

		gismeteo, ok := byProvider[domain.ProviderGismeteo]
		require.True(t, ok, "gismeteo row must be present")
		assert.Equal(t, "Gismeteo", gismeteo.Title)
		assert.True(t, gismeteo.Active)
		assert.Equal(t, "https://www.gismeteo.kz", gismeteo.BaseURL)
		assert.Equal(t, "3h", gismeteo.ThrottleInterval)
		assert.Equal(t, wantGismeteoUA, gismeteo.Options.UserAgent,
			"seeded User-Agent must match the gismeteoUserAgent constant byte-for-byte")
	})

	t.Run("malformed options JSON returns a wrapped error", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)

		tx, err := db.Transaction(t.Context())
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(),
			"UPDATE "+weatherSourceTableName+" SET "+weatherSourceOptionsFieldName+
				" = 'not-json' WHERE "+weatherSourceProviderFieldName+" = ?", domain.ProviderGismeteo)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())

		repo, err := NewWeatherSourceRepository(db)
		require.NoError(t, err)

		_, err = repo.ObtainAllWeatherSources(t.Context())
		require.Error(t, err, "a row with invalid options JSON must surface a wrapped error, not panic")
		assert.Contains(t, err.Error(), "unmarshal options")
	})
}

func TestWeatherSourceRepository_ObtainWeatherSourceByProvider(t *testing.T) {
	t.Parallel()

	t.Run("returns the gismeteo row with parsed options", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherSourceRepository(db)
		require.NoError(t, err)

		got, err := repo.ObtainWeatherSourceByProvider(t.Context(), domain.ProviderGismeteo)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, domain.ProviderGismeteo, got.Provider)
		assert.True(t, got.Active)
		assert.Equal(t, "https://www.gismeteo.kz", got.BaseURL)
		assert.Equal(t, "3h", got.ThrottleInterval)
		assert.Equal(t, wantGismeteoUA, got.Options.UserAgent)
	})

	t.Run("returns the open-meteo row", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherSourceRepository(db)
		require.NoError(t, err)

		got, err := repo.ObtainWeatherSourceByProvider(t.Context(), domain.ProviderOpenMeteo)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, domain.ProviderOpenMeteo, got.Provider)
		assert.True(t, got.Active)
	})

	t.Run("returns (nil, nil) for an unknown provider", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherSourceRepository(db)
		require.NoError(t, err)

		got, err := repo.ObtainWeatherSourceByProvider(t.Context(), "nope")
		require.NoError(t, err, "absence must not be an error")
		assert.Nil(t, got)
	})
}

func TestWeatherSourceRepository_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("succeeds against the seeded table", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherSourceRepository(db)
		require.NoError(t, err)

		require.NoError(t, repo.CheckUP(t.Context()))
	})

	t.Run("returns error when the DB is unavailable", func(t *testing.T) {
		t.Parallel()
		repo, err := NewWeatherSourceRepository(&mockFailDB{err: assert.AnError})
		require.NoError(t, err)

		require.Error(t, repo.CheckUP(t.Context()))
	})
}
