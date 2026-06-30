package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeatherUserCityRepository_RetainWeatherUserCity(t *testing.T) {
	t.Parallel()

	t.Run("inserts new city and round-trips all fields", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:    domain.UserTypeTelegram,
			UserID:      "u1",
			LocationID:  "loc1",
			DisplayName: "Almaty",
			Latitude:    43.2525,
			Longitude:   76.9115,
			Timezone:    "Asia/Almaty",
			Country:     "Kazakhstan",
			Admin1:      "Almaty",
			NotifyKind:  domain.WeatherNotifyMorningSummary,
			NotifyHour:  7,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))
		require.NotEmpty(t, city.ID)

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, city.ID, got.ID)
		assert.Equal(t, domain.UserTypeTelegram, got.UserType)
		assert.Equal(t, "u1", got.UserID)
		assert.Equal(t, "loc1", got.LocationID)
		assert.Equal(t, "Almaty", got.DisplayName)
		assert.InDelta(t, 43.2525, got.Latitude, 1e-4)
		assert.InDelta(t, 76.9115, got.Longitude, 1e-4)
		assert.Equal(t, "Asia/Almaty", got.Timezone)
		assert.Equal(t, "Kazakhstan", got.Country)
		assert.Equal(t, "Almaty", got.Admin1)
		assert.Nil(t, got.GismeteoCityID)
		assert.Equal(t, domain.WeatherNotifyMorningSummary, got.NotifyKind)
		assert.Equal(t, 7, got.NotifyHour)
		assert.True(t, got.LastNotifiedAt.IsZero())
		assert.False(t, got.CreatedAt.IsZero())
		assert.False(t, got.UpdatedAt.IsZero())
	})

	t.Run("re-subscribe on same unique key updates in place", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "u2",
			LocationID: "loc2",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
			NotifyHour: 8,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))
		firstID := city.ID
		require.NotEmpty(t, firstID)

		city2 := &domain.WeatherUserCity{
			UserType:    domain.UserTypeTelegram,
			UserID:      "u2",
			LocationID:  "loc2",
			DisplayName: "Updated",
			Timezone:    "UTC",
			NotifyKind:  domain.WeatherNotifyMorningSummary,
			NotifyHour:  9,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city2))

		// RETURNING ensures city2.ID reflects the original stored id, not a phantom.
		assert.Equal(t, firstID, city2.ID, "re-subscribe: record.ID must be the ORIGINAL id, not a newly-minted phantom")

		all, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "u2")
		require.NoError(t, err)
		require.Len(t, all, 1, "re-subscribe must not create a second row")
		assert.Equal(t, firstID, all[0].ID, "original ID must be preserved on conflict")
		assert.Equal(t, 9, all[0].NotifyHour)
		assert.Equal(t, "Updated", all[0].DisplayName)

		// Verify the original id is still findable by ObtainWeatherUserCityByID.
		found, err := repo.ObtainWeatherUserCityByID(t.Context(), firstID)
		require.NoError(t, err)
		assert.Equal(t, firstID, found.ID)
		assert.Equal(t, 9, found.NotifyHour)
	})

	t.Run("stores and retrieves gismeteo_city_id", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		gisID := 12345
		city := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         "u3",
			LocationID:     "loc3",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyMorningSummary,
			GismeteoCityID: &gisID,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		require.NotNil(t, got.GismeteoCityID)
		assert.Equal(t, gisID, *got.GismeteoCityID)
	})

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)
		require.Error(t, repo.RetainWeatherUserCity(t.Context(), nil))
	})
}

func TestWeatherUserCityRepository_ObtainWeatherUserCitiesByUserID(t *testing.T) {
	t.Parallel()

	t.Run("returns empty slice when no cities", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		items, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "nobody")
		require.NoError(t, err)
		require.NotNil(t, items)
		assert.Empty(t, items)
	})

	t.Run("returns only the requesting user's cities", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		for _, uid := range []string{"user-a", "user-b"} {
			require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &domain.WeatherUserCity{
				UserType:   domain.UserTypeTelegram,
				UserID:     uid,
				LocationID: "loc-" + uid,
				Timezone:   "UTC",
				NotifyKind: domain.WeatherNotifyMorningSummary,
			}))
		}

		items, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "user-a")
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "user-a", items[0].UserID)
	})
}

func TestWeatherUserCityRepository_ObtainWeatherUserCityByID(t *testing.T) {
	t.Parallel()

	t.Run("missing ID returns ErrNotFound", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), "nonexistent")
		require.Nil(t, got)
		require.True(t, errors.Is(err, internal.ErrNotFound))
	})
}

func TestWeatherUserCityRepository_RemoveWeatherUserCity(t *testing.T) {
	t.Parallel()

	t.Run("deletes existing row", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "u-del",
			LocationID: "loc-del",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))

		require.NoError(t, repo.RemoveWeatherUserCity(t.Context(), city))

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.Nil(t, got)
		require.True(t, errors.Is(err, internal.ErrNotFound))
	})

	t.Run("nil record returns error", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)
		require.Error(t, repo.RemoveWeatherUserCity(t.Context(), nil))
	})
}

func TestWeatherUserCityRepository_ObtainDistinctWeatherLocations(t *testing.T) {
	t.Parallel()

	t.Run("empty table returns empty slice", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		items, err := repo.ObtainDistinctWeatherLocations(t.Context())
		require.NoError(t, err)
		assert.NotNil(t, items)
		assert.Empty(t, items)
	})

	t.Run("two users same location returns one entry", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		for _, uid := range []string{"ua", "ub"} {
			require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &domain.WeatherUserCity{
				UserType:   domain.UserTypeTelegram,
				UserID:     uid,
				LocationID: "shared-loc",
				Latitude:   43.25,
				Longitude:  76.91,
				Timezone:   "UTC",
				NotifyKind: domain.WeatherNotifyMorningSummary,
			}))
		}

		items, err := repo.ObtainDistinctWeatherLocations(t.Context())
		require.NoError(t, err)
		require.Len(t, items, 1, "distinct must collapse two users on same location to one")
		assert.Equal(t, "shared-loc", items[0].LocationID)
	})

	t.Run("two distinct locations returns two entries", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		for i, loc := range []string{"loc-x", "loc-y"} {
			require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &domain.WeatherUserCity{
				UserType:   domain.UserTypeTelegram,
				UserID:     "u-distinct",
				LocationID: loc,
				Timezone:   "UTC",
				NotifyKind: domain.WeatherNotifyMorningSummary,
				NotifyHour: i + 7,
			}))
		}

		items, err := repo.ObtainDistinctWeatherLocations(t.Context())
		require.NoError(t, err)
		require.Len(t, items, 2)
	})
}

func TestWeatherUserCityRepository_ObtainDueWeatherUserCities(t *testing.T) {
	t.Parallel()

	t.Run("returns cities matching notify kind", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "u-due",
			LocationID: "loc-due",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
		}))

		items, err := repo.ObtainDueWeatherUserCities(t.Context(), domain.WeatherNotifyMorningSummary)
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "u-due", items[0].UserID)
	})

	t.Run("returns empty slice for unknown kind", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		items, err := repo.ObtainDueWeatherUserCities(t.Context(), "nonexistent_kind")
		require.NoError(t, err)
		assert.Empty(t, items)
	})
}

func TestWeatherUserCityRepository_AdvanceLastNotifiedAt(t *testing.T) {
	t.Parallel()

	t.Run("updates last_notified_at", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "u-adv",
			LocationID: "loc-adv",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))

		when := time.Now().UTC().Truncate(time.Second)
		require.NoError(t, repo.AdvanceLastNotifiedAt(t.Context(), city.ID, when))

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, when.Format(time.RFC3339), got.LastNotifiedAt.Format(time.RFC3339))
	})
}

func TestWeatherUserCityRepository_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("succeeds on migrated db", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)
		require.NoError(t, repo.CheckUP(t.Context()))
	})

	t.Run("fails on db error", func(t *testing.T) {
		t.Parallel()
		repo := &WeatherUserCityRepository{db: &mockFailDB{err: errors.New("db unavailable")}}
		require.Error(t, repo.CheckUP(t.Context()))
	})
}
