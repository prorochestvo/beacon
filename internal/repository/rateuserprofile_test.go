package repository

import (
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"

	"github.com/stretchr/testify/require"
)

func TestRateUserProfileRepository_UpsertRateUserProfile(t *testing.T) {
	t.Parallel()

	t.Run("inserts then updates existing row", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewRateUserProfileRepository(db)
		require.NoError(t, err)

		err = repo.UpsertRateUserProfile(t.Context(), &domain.RateUserProfile{
			UserType: domain.UserTypeTelegram,
			UserID:   "115818690",
			Timezone: "Asia/Almaty",
			Locale:   "kk-KZ",
		})
		require.NoError(t, err)

		got, err := repo.ObtainRateUserProfileByUserID(t.Context(), domain.UserTypeTelegram, "115818690")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "Asia/Almaty", got.Timezone)
		require.Equal(t, "kk-KZ", got.Locale)
		firstCreatedAt := got.CreatedAt
		require.False(t, firstCreatedAt.IsZero())

		// Sleep one second so the UpdatedAt change is observable through
		// the 1-second RFC3339 truncation.
		time.Sleep(1100 * time.Millisecond)

		err = repo.UpsertRateUserProfile(t.Context(), &domain.RateUserProfile{
			UserType: domain.UserTypeTelegram,
			UserID:   "115818690",
			Timezone: "Europe/Moscow",
			Locale:   "ru-RU",
		})
		require.NoError(t, err)

		got, err = repo.ObtainRateUserProfileByUserID(t.Context(), domain.UserTypeTelegram, "115818690")
		require.NoError(t, err)
		require.Equal(t, "Europe/Moscow", got.Timezone)
		require.Equal(t, "ru-RU", got.Locale, "locale must be overwritten on conflict")
		require.True(t, got.UpdatedAt.After(firstCreatedAt),
			"updated_at must advance on conflict")
		require.True(t, got.CreatedAt.Equal(firstCreatedAt),
			"created_at must not change on conflict")
	})

	t.Run("empty locale stored as empty string", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewRateUserProfileRepository(db)
		require.NoError(t, err)

		err = repo.UpsertRateUserProfile(t.Context(), &domain.RateUserProfile{
			UserType: domain.UserTypeTelegram,
			UserID:   "no-locale",
			Timezone: "UTC",
			// Locale intentionally omitted.
		})
		require.NoError(t, err)

		got, err := repo.ObtainRateUserProfileByUserID(t.Context(), domain.UserTypeTelegram, "no-locale")
		require.NoError(t, err)
		require.Empty(t, got.Locale)
	})

	t.Run("rejects unknown IANA name with PublicError", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewRateUserProfileRepository(db)
		require.NoError(t, err)

		err = repo.UpsertRateUserProfile(t.Context(), &domain.RateUserProfile{
			UserType: domain.UserTypeTelegram,
			UserID:   "u1",
			Timezone: "Atlantis/Atlantis",
		})
		require.Error(t, err)
		var pub *internal.PublicError
		require.ErrorAs(t, err, &pub)
	})

	t.Run("rejects empty identity", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewRateUserProfileRepository(db)
		require.NoError(t, err)

		err = repo.UpsertRateUserProfile(t.Context(), &domain.RateUserProfile{
			UserType: "",
			UserID:   "",
			Timezone: "UTC",
		})
		var pub *internal.PublicError
		require.ErrorAs(t, err, &pub)
	})

	t.Run("accepts UTC", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewRateUserProfileRepository(db)
		require.NoError(t, err)

		require.NoError(t, repo.UpsertRateUserProfile(t.Context(), &domain.RateUserProfile{
			UserType: domain.UserTypeTelegram,
			UserID:   "u2",
			Timezone: "UTC",
		}))
	})
}

func TestRateUserProfileRepository_ObtainRateUserProfileByUserID(t *testing.T) {
	t.Parallel()

	t.Run("missing row returns ErrNotFound", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewRateUserProfileRepository(db)
		require.NoError(t, err)

		got, err := repo.ObtainRateUserProfileByUserID(t.Context(), domain.UserTypeTelegram, "nobody")
		require.Nil(t, got)
		require.ErrorIs(t, err, internal.ErrNotFound)
	})
}
