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
		assert.False(t, got.AlertLatched, "a fresh insert must default alert_latched to armed (false)")
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

	t.Run("re-subscribe on conflict preserves an already-latched alert_latched", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         "u-relatch",
			LocationID:     "loc-relatch",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertFrost,
			ConditionValue: "0",
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))
		require.NoError(t, repo.SetWeatherAlertLatched(t.Context(), city.ID, true))

		// Re-subscribe (ON CONFLICT update) with the same unique key.
		resubscribe := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         "u-relatch",
			LocationID:     "loc-relatch",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertFrost,
			ConditionValue: "-5",
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), resubscribe))
		assert.Equal(t, city.ID, resubscribe.ID, "re-subscribe must update the original row")

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		assert.True(t, got.AlertLatched, "alert_latched is absent from RetainWeatherUserCity's ON CONFLICT DO UPDATE SET clause, so a re-subscribe must not clobber it")
	})

	t.Run("alert_latched true is persisted on insert", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:     domain.UserTypeTelegram,
			UserID:       "u-prelatch",
			LocationID:   "loc-prelatch",
			Timezone:     "UTC",
			NotifyKind:   domain.WeatherNotifyAlertThaw,
			AlertLatched: true,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		assert.True(t, got.AlertLatched, "a fresh insert must persist AlertLatched=true when the caller sets it (e.g. the forced thaw row)")
	})

	t.Run("re-subscribe does not overwrite alert_latched even when the payload sets it true", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "u-armed",
			LocationID: "loc-armed",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyAlertThaw,
			// AlertLatched omitted (false): simulates a row that has re-armed since it
			// was first created latched.
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))

		before, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		require.False(t, before.AlertLatched)

		// A re-subscribe / ensure-thaw retain on the same unique key, requesting
		// AlertLatched=true, must not stomp the row's real (re-armed) latch state —
		// alert_latched is insert-only and absent from the ON CONFLICT SET clause.
		resubscribe := &domain.WeatherUserCity{
			UserType:     domain.UserTypeTelegram,
			UserID:       "u-armed",
			LocationID:   "loc-armed",
			Timezone:     "UTC",
			NotifyKind:   domain.WeatherNotifyAlertThaw,
			AlertLatched: true,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), resubscribe))

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		assert.False(t, got.AlertLatched, "ON CONFLICT must never write alert_latched, regardless of what the incoming payload requests")
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

	t.Run("condition_value round-trips for alert_heat", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         "u-heat",
			LocationID:     "loc-heat",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertHeat,
			ConditionValue: "35.5",
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))
		require.NotEmpty(t, city.ID)

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		assert.Equal(t, "35.5", got.ConditionValue)
		assert.Equal(t, domain.WeatherNotifyAlertHeat, got.NotifyKind)
	})

	t.Run("condition_value round-trips for alert_frost with negative threshold", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         "u-frost",
			LocationID:     "loc-frost",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertFrost,
			ConditionValue: "-5",
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		assert.Equal(t, "-5", got.ConditionValue)
		assert.Equal(t, domain.WeatherNotifyAlertFrost, got.NotifyKind)
	})

	t.Run("condition_value round-trips as empty for alert_thunderstorm", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         "u-storm",
			LocationID:     "loc-storm",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertThunderstorm,
			ConditionValue: "",
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		assert.Equal(t, "", got.ConditionValue)
		assert.Equal(t, domain.WeatherNotifyAlertThunderstorm, got.NotifyKind)
	})

	t.Run("same user, same location, different notify_kind creates separate rows", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		base := domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "u-multi",
			LocationID: "loc-multi",
			Timezone:   "UTC",
		}

		morning := base
		morning.NotifyKind = domain.WeatherNotifyMorningSummary
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &morning))

		heat := base
		heat.NotifyKind = domain.WeatherNotifyAlertHeat
		heat.ConditionValue = "36"
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &heat))

		frost := base
		frost.NotifyKind = domain.WeatherNotifyAlertFrost
		frost.ConditionValue = "0"
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &frost))

		all, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "u-multi")
		require.NoError(t, err)
		assert.Len(t, all, 3, "one row per notify_kind for the same (user, location) pair")
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

func TestWeatherUserCityRepository_RemoveWeatherUserCitiesByLocation(t *testing.T) {
	t.Parallel()

	t.Run("deletes every row for the target location, other location and other user survive", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		for _, kind := range []domain.WeatherNotifyKind{
			domain.WeatherNotifyMorningSummary,
			domain.WeatherNotifyAlertHeat,
			domain.WeatherNotifyAlertThaw,
		} {
			require.NoError(t, repo.RetainWeatherUserCity(t.Context(), &domain.WeatherUserCity{
				UserType:   domain.UserTypeTelegram,
				UserID:     "target-user",
				LocationID: "target-loc",
				Timezone:   "UTC",
				NotifyKind: kind,
			}))
		}

		otherLocation := &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "target-user",
			LocationID: "other-loc",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), otherLocation))

		otherUser := &domain.WeatherUserCity{
			UserType:   domain.UserTypeTelegram,
			UserID:     "other-user",
			LocationID: "target-loc",
			Timezone:   "UTC",
			NotifyKind: domain.WeatherNotifyMorningSummary,
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), otherUser))

		require.NoError(t, repo.RemoveWeatherUserCitiesByLocation(t.Context(), domain.UserTypeTelegram, "target-user", "target-loc"))

		remaining, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "target-user")
		require.NoError(t, err)
		require.Len(t, remaining, 1, "only the other-location row for target-user must survive")
		assert.Equal(t, "other-loc", remaining[0].LocationID)

		otherUserRows, err := repo.ObtainWeatherUserCitiesByUserID(t.Context(), domain.UserTypeTelegram, "other-user")
		require.NoError(t, err)
		require.Len(t, otherUserRows, 1, "another user's row at the same location_id must survive")
	})

	t.Run("no matching rows returns ErrNotFound", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		err = repo.RemoveWeatherUserCitiesByLocation(t.Context(), domain.UserTypeTelegram, "nobody", "nowhere")
		require.True(t, errors.Is(err, internal.ErrNotFound))
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

func TestWeatherUserCityRepository_SetWeatherAlertLatched(t *testing.T) {
	t.Parallel()

	t.Run("sets and clears the latch without touching updated_at or last_notified_at", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         "u-latch",
			LocationID:     "loc-latch",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertFrost,
			ConditionValue: "0",
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))

		before, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		require.False(t, before.AlertLatched)

		require.NoError(t, repo.SetWeatherAlertLatched(t.Context(), city.ID, true))
		afterSet, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		assert.True(t, afterSet.AlertLatched)
		assert.Equal(t, before.UpdatedAt.Format(time.RFC3339), afterSet.UpdatedAt.Format(time.RFC3339),
			"the latch is system state, not a user edit: updated_at must not change")
		assert.True(t, afterSet.LastNotifiedAt.IsZero(), "a latch-only write must not touch last_notified_at")

		require.NoError(t, repo.SetWeatherAlertLatched(t.Context(), city.ID, false))
		afterClear, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		assert.False(t, afterClear.AlertLatched)
		assert.Equal(t, before.UpdatedAt.Format(time.RFC3339), afterClear.UpdatedAt.Format(time.RFC3339))
		assert.True(t, afterClear.LastNotifiedAt.IsZero())
	})
}

func TestWeatherUserCityRepository_MarkWeatherAlertFired(t *testing.T) {
	t.Parallel()

	t.Run("latches and records the forecast_date fire cursor without touching updated_at", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewWeatherUserCityRepository(db)
		require.NoError(t, err)

		city := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         "u-fired",
			LocationID:     "loc-fired",
			Timezone:       "UTC",
			NotifyKind:     domain.WeatherNotifyAlertFrost,
			ConditionValue: "0",
		}
		require.NoError(t, repo.RetainWeatherUserCity(t.Context(), city))

		before, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)

		key, err := domain.ForecastDateKey("2026-01-15")
		require.NoError(t, err)
		require.NoError(t, repo.MarkWeatherAlertFired(t.Context(), city.ID, key))

		got, err := repo.ObtainWeatherUserCityByID(t.Context(), city.ID)
		require.NoError(t, err)
		assert.True(t, got.AlertLatched)
		assert.True(t, key.Equal(got.LastNotifiedAt), "last_notified_at must equal the stored forecast_date key")
		assert.Equal(t, before.UpdatedAt.Format(time.RFC3339), got.UpdatedAt.Format(time.RFC3339))
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
