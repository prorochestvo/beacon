package profile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ Store = (*stubProfiles)(nil)

// stubProfiles records the record it was handed and can fail the write, so a
// test can check both what was stored and how a store failure travels.
type stubProfiles struct {
	err error

	upserted *domain.RateUserProfile
}

func (s *stubProfiles) UpsertRateUserProfile(_ context.Context, record *domain.RateUserProfile) error {
	s.upserted = record
	return s.err
}

func TestService_UpsertMeProfile(t *testing.T) {
	t.Parallel()

	const caller = "42"

	t.Run("stores the trimmed values under the caller", func(t *testing.T) {
		t.Parallel()

		profiles := &stubProfiles{}
		require.NoError(t, NewService(profiles).UpsertMeProfile(t.Context(), caller, Profile{
			Timezone: "  Asia/Almaty  ",
			Locale:   "  ru-RU  ",
		}))

		require.NotNil(t, profiles.upserted)
		assert.Equal(t, domain.UserTypeTelegram, profiles.upserted.UserType)
		assert.Equal(t, caller, profiles.upserted.UserID, "the profile belongs to the caller, never to a value in the request")
		assert.Equal(t, "Asia/Almaty", profiles.upserted.Timezone)
		assert.Equal(t, "ru-RU", profiles.upserted.Locale)
	})

	t.Run("an empty locale is stored as empty, not refused", func(t *testing.T) {
		t.Parallel()

		profiles := &stubProfiles{}
		require.NoError(t, NewService(profiles).UpsertMeProfile(t.Context(), caller, Profile{Timezone: "UTC"}))

		require.NotNil(t, profiles.upserted)
		assert.Empty(t, profiles.upserted.Locale)
	})

	t.Run("a missing timezone is refused before anything is written", func(t *testing.T) {
		t.Parallel()

		absent := map[string]string{
			"empty":           "",
			"whitespace only": "   ",
		}
		for name, timezone := range absent {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				profiles := &stubProfiles{}
				err := NewService(profiles).UpsertMeProfile(t.Context(), caller, Profile{Timezone: timezone})

				var pub *internal.PublicError
				require.ErrorAs(t, err, &pub)
				assert.Equal(t, "timezone is required", pub.Details())
				assert.Nil(t, profiles.upserted)
			})
		}
	})

	t.Run("an oversized locale is refused", func(t *testing.T) {
		t.Parallel()

		profiles := &stubProfiles{}
		err := NewService(profiles).UpsertMeProfile(t.Context(), caller, Profile{
			Timezone: "UTC",
			Locale:   strings.Repeat("x", maxLocaleLength+1),
		})

		var pub *internal.PublicError
		require.ErrorAs(t, err, &pub)
		assert.Equal(t, "locale too long", pub.Details())
		assert.Nil(t, profiles.upserted)
	})

	t.Run("a locale at the limit is accepted", func(t *testing.T) {
		t.Parallel()

		profiles := &stubProfiles{}
		require.NoError(t, NewService(profiles).UpsertMeProfile(t.Context(), caller, Profile{
			Timezone: "UTC",
			Locale:   strings.Repeat("x", maxLocaleLength),
		}))
		require.NotNil(t, profiles.upserted)
	})

	t.Run("the store's own rejection stays showable to the caller", func(t *testing.T) {
		t.Parallel()

		// A timezone that will not load is caught by the store, which is what has
		// to read the value back; its answer must not be flattened into a 500.
		profiles := &stubProfiles{err: internal.NewPublicError("Invalid timezone.")}
		err := NewService(profiles).UpsertMeProfile(t.Context(), caller, Profile{Timezone: "Not/AZone"})

		var pub *internal.PublicError
		require.ErrorAs(t, err, &pub)
		assert.Equal(t, "Invalid timezone.", pub.Details())
	})

	t.Run("a store failure is reported as a failure", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		err := NewService(&stubProfiles{err: down}).UpsertMeProfile(t.Context(), caller, Profile{Timezone: "UTC"})

		require.ErrorIs(t, err, down)
		var pub *internal.PublicError
		require.NotErrorAs(t, err, &pub, "a dead database is not something to tell the caller about")
	})
}
