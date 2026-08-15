package weather

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ CitiesLoader       = (*stubCities)(nil)
	_ ObservationsLoader = (*stubObservations)(nil)
)

// stubCities serves a fixed city list and records the (userType, userID) pair it
// was asked for, so a test can prove the read is scoped to one caller.
type stubCities struct {
	cities []domain.WeatherUserCity
	err    error

	askedUserType domain.UserType
	askedUserID   string
}

func (s *stubCities) ObtainWeatherUserCitiesByUserID(_ context.Context, userType domain.UserType, userID string) ([]domain.WeatherUserCity, error) {
	s.askedUserType = userType
	s.askedUserID = userID
	if s.err != nil {
		return nil, s.err
	}
	return s.cities, nil
}

// stubObservations answers from obs, reporting internal.ErrNotFound for a
// location it holds nothing for — the repository's contract for a city whose
// first collection has not run. It counts calls so a test can prove one
// location is read once however many notify kinds point at it.
type stubObservations struct {
	obs map[string]*domain.WeatherObservation
	err error

	calls int
}

func (s *stubObservations) ObtainLatestObservation(_ context.Context, locationID, _ string) (*domain.WeatherObservation, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if o, ok := s.obs[locationID]; ok {
		return o, nil
	}
	return nil, internal.ErrNotFound
}

func TestService_ObtainMeCities(t *testing.T) {
	t.Parallel()

	t.Run("returns the caller's rows, one per notify kind", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{cities: []domain.WeatherUserCity{
			{ID: "c1", LocationID: "1234", DisplayName: "Almaty", NotifyKind: domain.WeatherNotifyMorningSummary},
			{ID: "c2", LocationID: "1234", DisplayName: "Almaty", NotifyKind: domain.WeatherNotifyAlertThaw},
		}}

		got, err := NewService(cities, &stubObservations{}).ObtainMeCities(t.Context(), "42")
		require.NoError(t, err)

		require.Len(t, got, 2, "the city list is per subscription, not per location")
		assert.Equal(t, domain.UserTypeTelegram, cities.askedUserType)
		assert.Equal(t, "42", cities.askedUserID)
	})

	t.Run("a store failure is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		got, err := NewService(&stubCities{err: down}, &stubObservations{}).ObtainMeCities(t.Context(), "42")
		require.ErrorIs(t, err, down)
		assert.Nil(t, got)
	})
}

func TestService_ObtainMeCurrent(t *testing.T) {
	t.Parallel()

	newCity := func(locationID, displayName string) domain.WeatherUserCity {
		return domain.WeatherUserCity{
			ID:          "city-" + locationID,
			UserType:    domain.UserTypeTelegram,
			UserID:      "42",
			LocationID:  locationID,
			DisplayName: displayName,
			Timezone:    "Asia/Almaty",
			NotifyKind:  domain.WeatherNotifyMorningSummary,
			NotifyHour:  7,
		}
	}

	newObs := func(locationID string) *domain.WeatherObservation {
		temp := 25.5
		return &domain.WeatherObservation{
			ID:          "obs-" + locationID,
			LocationID:  locationID,
			Provider:    domain.ProviderOpenMeteo,
			TempCurrent: &temp,
			CapturedAt:  time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
		}
	}

	t.Run("pairs each city with its latest observation", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{cities: []domain.WeatherUserCity{newCity("1234", "Almaty")}}
		obs := &stubObservations{obs: map[string]*domain.WeatherObservation{"1234": newObs("1234")}}

		got, err := NewService(cities, obs).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)

		require.Len(t, got, 1)
		assert.Equal(t, "1234", got[0].City.LocationID)
		require.NotNil(t, got[0].Observation)
		assert.Equal(t, "obs-1234", got[0].Observation.ID)
	})

	t.Run("one location read once however many notify kinds point at it", func(t *testing.T) {
		t.Parallel()

		first := newCity("1234", "Almaty")
		second := first
		second.ID = "city-1234-b"
		second.NotifyKind = domain.WeatherNotifyAlertThaw

		cities := &stubCities{cities: []domain.WeatherUserCity{first, second}}
		obs := &stubObservations{obs: map[string]*domain.WeatherObservation{"1234": newObs("1234")}}

		got, err := NewService(cities, obs).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)

		require.Len(t, got, 1, "a physical city has one set of readings regardless of how many alerts watch it")
		assert.Equal(t, "city-1234", got[0].City.ID, "the first row seen for a location is the one carried")
		assert.Equal(t, 1, obs.calls, "deduplicating after the reads would still pay for them")
	})

	t.Run("a city with nothing collected yet is listed with no observation", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{cities: []domain.WeatherUserCity{newCity("1234", "Almaty")}}

		got, err := NewService(cities, &stubObservations{}).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)

		require.Len(t, got, 1, "a just-added city must stay visible while its first collection is pending")
		assert.Nil(t, got[0].Observation)
	})

	t.Run("no cities yields an empty slice", func(t *testing.T) {
		t.Parallel()

		got, err := NewService(&stubCities{}, &stubObservations{}).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("a failure in either store is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		broken := map[string]*Service{
			"cities": NewService(&stubCities{err: down}, &stubObservations{}),
			"observations": NewService(
				&stubCities{cities: []domain.WeatherUserCity{newCity("1234", "Almaty")}},
				&stubObservations{err: down},
			),
		}
		for name, svc := range broken {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				got, err := svc.ObtainMeCurrent(t.Context(), "42")
				require.ErrorIs(t, err, down)
				assert.Nil(t, got)
			})
		}
	})
}
