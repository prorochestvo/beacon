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
	_ CitiesStore        = (*stubCities)(nil)
	_ ObservationsLoader = (*stubObservations)(nil)
	_ ForecastLoader     = (*stubForecasts)(nil)
)

// stubCities serves a fixed city list and records the (userType, userID) pair it
// was asked for, so a test can prove the read is scoped to one caller.
type stubCities struct {
	cities []domain.WeatherUserCity
	byID   map[string]*domain.WeatherUserCity
	err    error

	getErr    error
	retainErr error
	removeErr error

	// retainErrForKind, when non-nil, fails only the retain whose record carries
	// retainErrOnKind — the shape of a forced row failing after the requested one
	// was written.
	retainErrOnKind  domain.WeatherNotifyKind
	retainErrForKind error

	retained          []*domain.WeatherUserCity
	removed           []*domain.WeatherUserCity
	removedByLocation []removeByLocationCall

	removeByLocationErr error

	askedUserType domain.UserType
	askedUserID   string
}

// removeByLocationCall records one RemoveWeatherUserCitiesByLocation invocation.
type removeByLocationCall struct {
	userType   domain.UserType
	userID     string
	locationID string
}

func (s *stubCities) ObtainWeatherUserCityByID(_ context.Context, id string) (*domain.WeatherUserCity, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if city, ok := s.byID[id]; ok {
		return city, nil
	}
	return nil, internal.ErrNotFound
}

func (s *stubCities) RetainWeatherUserCity(_ context.Context, record *domain.WeatherUserCity) error {
	if s.retainErr != nil {
		return s.retainErr
	}
	if s.retainErrForKind != nil && record.NotifyKind == s.retainErrOnKind {
		return s.retainErrForKind
	}
	if record.ID == "" {
		record.ID = "generated-" + string(record.NotifyKind)
	}
	s.retained = append(s.retained, record)
	return nil
}

func (s *stubCities) RemoveWeatherUserCity(_ context.Context, record *domain.WeatherUserCity) error {
	if s.removeErr != nil {
		return s.removeErr
	}
	s.removed = append(s.removed, record)
	return nil
}

func (s *stubCities) RemoveWeatherUserCitiesByLocation(_ context.Context, userType domain.UserType, userID, locationID string) error {
	if s.removeByLocationErr != nil {
		return s.removeByLocationErr
	}
	s.removedByLocation = append(s.removedByLocation, removeByLocationCall{userType: userType, userID: userID, locationID: locationID})
	return nil
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
// stubForecasts stands in for the forecast-day repository. days is keyed by location_id;
// a location absent from the map has nothing stored, which is the normal state of a city
// whose first long-range fetch has not completed.
type stubForecasts struct {
	days map[string][]domain.WeatherForecastDay
	err  error

	calls int
}

func (s *stubForecasts) ObtainForecastDays(_ context.Context, locationID, _, _ string, _ int) ([]domain.WeatherForecastDay, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.days[locationID], nil
}

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

		got, err := NewService(cities, &stubObservations{}, &stubForecasts{}).ObtainMeCities(t.Context(), "42")
		require.NoError(t, err)

		require.Len(t, got, 2, "the city list is per subscription, not per location")
		assert.Equal(t, domain.UserTypeTelegram, cities.askedUserType)
		assert.Equal(t, "42", cities.askedUserID)
	})

	t.Run("a store failure is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		got, err := NewService(&stubCities{err: down}, &stubObservations{}, &stubForecasts{}).ObtainMeCities(t.Context(), "42")
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

		got, err := NewService(cities, obs, &stubForecasts{}).ObtainMeCurrent(t.Context(), "42")
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

		got, err := NewService(cities, obs, &stubForecasts{}).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)

		require.Len(t, got, 1, "a physical city has one set of readings regardless of how many alerts watch it")
		assert.Equal(t, "city-1234", got[0].City.ID, "the first row seen for a location is the one carried")
		assert.Equal(t, 1, obs.calls, "deduplicating after the reads would still pay for them")
	})

	t.Run("a city with nothing collected yet is listed with no observation", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{cities: []domain.WeatherUserCity{newCity("1234", "Almaty")}}

		got, err := NewService(cities, &stubObservations{}, &stubForecasts{}).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)

		require.Len(t, got, 1, "a just-added city must stay visible while its first collection is pending")
		assert.Nil(t, got[0].Observation)
	})

	t.Run("no cities yields an empty slice", func(t *testing.T) {
		t.Parallel()

		got, err := NewService(&stubCities{}, &stubObservations{}, &stubForecasts{}).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("a failure in either store is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		broken := map[string]*Service{
			"cities": NewService(&stubCities{err: down}, &stubObservations{}, &stubForecasts{}),
			"observations": NewService(
				&stubCities{cities: []domain.WeatherUserCity{newCity("1234", "Almaty")}},
				&stubObservations{err: down},
				&stubForecasts{},
			),
			"forecasts": NewService(
				&stubCities{cities: []domain.WeatherUserCity{newCity("1234", "Almaty")}},
				&stubObservations{},
				&stubForecasts{err: down},
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

// validCity is a request that passes every check, so a test can vary one field
// and know the failure it sees came from that field.
func validCity() NewCity {
	return NewCity{
		LocationID:  "1234",
		DisplayName: "Almaty",
		Latitude:    43.25,
		Longitude:   76.94,
		Timezone:    "Asia/Almaty",
		Country:     "Kazakhstan",
		Admin1:      "Almaty",
	}
}

func TestService_CreateMeCity_Validation(t *testing.T) {
	t.Parallel()

	const caller = "99"

	hour := func(h int) *int { return &h }

	rejected := map[string]struct {
		mutate func(*NewCity)
		says   string
	}{
		"empty location_id":       {func(c *NewCity) { c.LocationID = "  " }, "location_id is required"},
		"empty display_name":      {func(c *NewCity) { c.DisplayName = "  " }, "display_name is required"},
		"unloadable timezone":     {func(c *NewCity) { c.Timezone = "Not/AZone" }, "invalid timezone"},
		"latitude above 90":       {func(c *NewCity) { c.Latitude = 91 }, "latitude must be between -90 and 90"},
		"latitude below -90":      {func(c *NewCity) { c.Latitude = -91 }, "latitude must be between -90 and 90"},
		"longitude above 180":     {func(c *NewCity) { c.Longitude = 181 }, "longitude must be between -180 and 180"},
		"longitude below -180":    {func(c *NewCity) { c.Longitude = -181 }, "longitude must be between -180 and 180"},
		"notify_hour above 23":    {func(c *NewCity) { c.NotifyHour = hour(24) }, "notify_hour must be between 0 and 23"},
		"notify_hour below 0":     {func(c *NewCity) { c.NotifyHour = hour(-1) }, "notify_hour must be between 0 and 23"},
		"unknown notify_kind":     {func(c *NewCity) { c.NotifyKind = "bogus" }, "unknown notify_kind"},
		"heat with no threshold":  {func(c *NewCity) { c.NotifyKind = domain.WeatherNotifyAlertHeat }, "condition_value"},
		"heat with text":          {func(c *NewCity) { c.NotifyKind = domain.WeatherNotifyAlertHeat; c.ConditionValue = "hot" }, "condition_value"},
		"rain above 100 percent":  {func(c *NewCity) { c.NotifyKind = domain.WeatherNotifyAlertRain; c.ConditionValue = "101" }, "probability percent"},
		"rain at zero percent":    {func(c *NewCity) { c.NotifyKind = domain.WeatherNotifyAlertRain; c.ConditionValue = "0" }, "probability percent"},
		"rain below zero percent": {func(c *NewCity) { c.NotifyKind = domain.WeatherNotifyAlertRain; c.ConditionValue = "-5" }, "probability percent"},
	}

	for name, tc := range rejected {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := validCity()
			tc.mutate(&req)

			cities := &stubCities{}
			id, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, req)

			var pub *internal.PublicError
			require.ErrorAs(t, err, &pub, "every field here comes from the client, so a bad one has to be named back to it")
			assert.Contains(t, pub.Details(), tc.says)
			assert.Empty(t, id)
			assert.Empty(t, cities.retained, "nothing is written for a request that never validated")
		})
	}
}

func TestService_CreateMeCity(t *testing.T) {
	t.Parallel()

	const caller = "99"

	hour := func(h int) *int { return &h }

	// retainedKinds lists the notify kinds written, in order, so a test can state
	// what a single add produced without indexing into the slice by hand.
	retainedKinds := func(cities *stubCities) []domain.WeatherNotifyKind {
		kinds := make([]domain.WeatherNotifyKind, 0, len(cities.retained))
		for _, row := range cities.retained {
			kinds = append(kinds, row.NotifyKind)
		}
		return kinds
	}

	t.Run("stores the row under the caller with the fields it was given", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{}
		id, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, validCity())
		require.NoError(t, err)
		assert.NotEmpty(t, id)

		require.NotEmpty(t, cities.retained)
		stored := cities.retained[0]
		assert.Equal(t, domain.UserTypeTelegram, stored.UserType)
		assert.Equal(t, caller, stored.UserID, "ownership comes from the caller, never from the request")
		assert.Equal(t, "1234", stored.LocationID)
		assert.Equal(t, "Almaty", stored.DisplayName)
		assert.InDelta(t, 43.25, stored.Latitude, 0.0001)
		assert.InDelta(t, 76.94, stored.Longitude, 0.0001)
		assert.Equal(t, "Asia/Almaty", stored.Timezone)
		assert.Equal(t, "Kazakhstan", stored.Country)
		assert.Equal(t, "Almaty", stored.Admin1)
		assert.Equal(t, domain.WeatherNotifyMorningSummary, stored.NotifyKind, "an omitted kind is a morning summary")
		assert.Equal(t, 7, stored.NotifyHour, "an omitted hour takes the default")
	})

	t.Run("an explicit notify_hour of zero is a choice, not an omission", func(t *testing.T) {
		t.Parallel()

		req := validCity()
		req.NotifyHour = hour(0)

		cities := &stubCities{}
		_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, req)
		require.NoError(t, err)

		require.NotEmpty(t, cities.retained)
		assert.Equal(t, 0, cities.retained[0].NotifyHour)
	})

	t.Run("a threshold is kept for the kinds that use one", func(t *testing.T) {
		t.Parallel()

		withThreshold := map[string]struct {
			kind  domain.WeatherNotifyKind
			value string
		}{
			"heat":  {domain.WeatherNotifyAlertHeat, "35"},
			"frost": {domain.WeatherNotifyAlertFrost, "-15"},
			"rain":  {domain.WeatherNotifyAlertRain, "80"},
		}
		for name, tc := range withThreshold {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				req := validCity()
				req.NotifyKind = tc.kind
				req.ConditionValue = tc.value

				cities := &stubCities{}
				_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, req)
				require.NoError(t, err)

				require.NotEmpty(t, cities.retained)
				assert.Equal(t, tc.kind, cities.retained[0].NotifyKind)
				assert.Equal(t, tc.value, cities.retained[0].ConditionValue)
			})
		}
	})

	t.Run("a threshold is dropped for the kinds that have none", func(t *testing.T) {
		t.Parallel()

		withoutThreshold := map[string]domain.WeatherNotifyKind{
			"morning summary": domain.WeatherNotifyMorningSummary,
			"thaw":            domain.WeatherNotifyAlertThaw,
		}
		for name, kind := range withoutThreshold {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				req := validCity()
				req.NotifyKind = kind
				req.ConditionValue = "whatever the client sent"

				cities := &stubCities{}
				_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, req)
				require.NoError(t, err)

				require.NotEmpty(t, cities.retained)
				assert.Empty(t, cities.retained[0].ConditionValue,
					"free text stored against a kind that never reads it is a field waiting to be misread")
			})
		}
	})

	t.Run("a thunderstorm alert needs no threshold", func(t *testing.T) {
		t.Parallel()

		req := validCity()
		req.NotifyKind = domain.WeatherNotifyAlertThunderstorm

		cities := &stubCities{}
		_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, req)
		require.NoError(t, err)
		assert.Equal(t, domain.WeatherNotifyAlertThunderstorm, cities.retained[0].NotifyKind)
	})

	t.Run("any add gives the city both forced alert rows", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{}
		_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, validCity())
		require.NoError(t, err)

		require.Len(t, cities.retained, 3)
		assert.Equal(t, []domain.WeatherNotifyKind{
			domain.WeatherNotifyMorningSummary,
			domain.WeatherNotifyAlertThaw,
			domain.WeatherNotifyAlertRain,
		}, retainedKinds(cities))

		thaw, rain := cities.retained[1], cities.retained[2]
		assert.True(t, thaw.AlertLatched,
			"thaw fires on TempMax > 0 alone, so an armed row would fire on the first tick for a city already past freezing")
		assert.False(t, rain.AlertLatched,
			"rain notifies on both latch edges, so a pre-latched row would open with a spurious 'rain cleared'")
		assert.Equal(t, "60", rain.ConditionValue)
		assert.Equal(t, 7, rain.NotifyHour)
		assert.Equal(t, "1234", rain.LocationID)
		assert.Equal(t, "Almaty", rain.DisplayName, "the forced rows describe the same city as the requested one")
	})

	t.Run("the kind the request created is not ensured a second time", func(t *testing.T) {
		t.Parallel()

		requested := map[string]struct {
			kind      domain.WeatherNotifyKind
			value     string
			alsoAdded []domain.WeatherNotifyKind
		}{
			"thaw": {domain.WeatherNotifyAlertThaw, "", []domain.WeatherNotifyKind{domain.WeatherNotifyAlertRain}},
			"rain": {domain.WeatherNotifyAlertRain, "80", []domain.WeatherNotifyKind{domain.WeatherNotifyAlertThaw}},
		}
		for name, tc := range requested {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				req := validCity()
				req.NotifyKind = tc.kind
				req.ConditionValue = tc.value

				cities := &stubCities{}
				_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, req)
				require.NoError(t, err)

				assert.Equal(t, append([]domain.WeatherNotifyKind{tc.kind}, tc.alsoAdded...), retainedKinds(cities))
			})
		}
	})

	t.Run("an existing rain row is left alone, so a tuned threshold survives", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{cities: []domain.WeatherUserCity{{
			ID: "rain-1", UserType: domain.UserTypeTelegram, UserID: caller,
			LocationID: "1234", NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "20",
		}}}
		req := validCity()
		req.NotifyKind = domain.WeatherNotifyAlertHeat
		req.ConditionValue = "35"

		_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, req)
		require.NoError(t, err)

		assert.Equal(t, []domain.WeatherNotifyKind{
			domain.WeatherNotifyAlertHeat,
			domain.WeatherNotifyAlertThaw,
		}, retainedKinds(cities), "the upsert rewrites condition_value, so re-ensuring rain would reset a retuned threshold")
	})

	t.Run("a rain row at another location does not stand in for this one", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{cities: []domain.WeatherUserCity{{
			ID: "rain-elsewhere", UserType: domain.UserTypeTelegram, UserID: caller,
			LocationID: "9999", NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "20",
		}}}

		_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, validCity())
		require.NoError(t, err)

		assert.Contains(t, retainedKinds(cities), domain.WeatherNotifyAlertRain)
	})

	t.Run("the seeded rain threshold passes the domain's own check", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{}
		_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, validCity())
		require.NoError(t, err)

		require.Len(t, cities.retained, 3)
		require.NoError(t, cities.retained[2].Validate(),
			"the forced rows skip resolveNewCity, so a seed the domain rejects would only surface when an alert tried to fire")
	})

	t.Run("a failure ensuring a forced row is reported, and the requested row stands", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		cities := &stubCities{retainErrOnKind: domain.WeatherNotifyAlertThaw, retainErrForKind: down}

		id, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, validCity())
		require.ErrorIs(t, err, down)
		assert.Empty(t, id)
		require.Len(t, cities.retained, 1, "the requested row was written before the forced one failed")
		assert.Equal(t, domain.WeatherNotifyMorningSummary, cities.retained[0].NotifyKind)
	})

	t.Run("a failure looking up the existing rows is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		cities := &stubCities{err: down}

		_, err := NewService(cities, &stubObservations{}, &stubForecasts{}).CreateMeCity(t.Context(), caller, validCity())
		require.ErrorIs(t, err, down)
	})

	t.Run("a failure writing the requested row is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		id, err := NewService(&stubCities{retainErr: down}, &stubObservations{}, &stubForecasts{}).
			CreateMeCity(t.Context(), caller, validCity())
		require.ErrorIs(t, err, down)
		assert.Empty(t, id)
	})
}

func TestService_DeleteMeCity(t *testing.T) {
	t.Parallel()

	const caller = "33"
	const other = "44"

	stored := func() *stubCities {
		return &stubCities{byID: map[string]*domain.WeatherUserCity{
			"city-1": {
				ID: "city-1", UserType: domain.UserTypeTelegram, UserID: caller,
				LocationID: "1234", DisplayName: "Almaty", NotifyKind: domain.WeatherNotifyAlertHeat, ConditionValue: "35",
			},
			"city-thaw": {
				ID: "city-thaw", UserType: domain.UserTypeTelegram, UserID: caller,
				LocationID: "1234", DisplayName: "Almaty", NotifyKind: domain.WeatherNotifyAlertThaw,
			},
			"city-rain": {
				ID: "city-rain", UserType: domain.UserTypeTelegram, UserID: caller,
				LocationID: "1234", DisplayName: "Almaty", NotifyKind: domain.WeatherNotifyAlertRain, ConditionValue: "60",
			},
			"city-other": {
				ID: "city-other", UserType: domain.UserTypeTelegram, UserID: other,
				LocationID: "5678", DisplayName: "Moscow", NotifyKind: domain.WeatherNotifyMorningSummary,
			},
			"city-other-thaw": {
				ID: "city-other-thaw", UserType: domain.UserTypeTelegram, UserID: other,
				LocationID: "5678", DisplayName: "Moscow", NotifyKind: domain.WeatherNotifyAlertThaw,
			},
		}}
	}

	t.Run("removes the stored row", func(t *testing.T) {
		t.Parallel()

		cities := stored()
		require.NoError(t, NewService(cities, &stubObservations{}, &stubForecasts{}).DeleteMeCity(t.Context(), caller, "city-1"))

		require.Len(t, cities.removed, 1)
		assert.Equal(t, "city-1", cities.removed[0].ID)
	})

	t.Run("a missing row and another user's row are the same answer", func(t *testing.T) {
		t.Parallel()

		unreachable := map[string]string{
			"no such city":          "no-such",
			"owned by another user": "city-other",
		}
		for name, id := range unreachable {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				cities := stored()
				err := NewService(cities, &stubObservations{}, &stubForecasts{}).DeleteMeCity(t.Context(), caller, id)

				require.ErrorIs(t, err, internal.ErrNotFound)
				assert.Empty(t, cities.removed)
			})
		}
	})

	t.Run("a forced row is refused with the explanation, and survives", func(t *testing.T) {
		t.Parallel()

		forced := map[string]string{
			"thaw": "city-thaw",
			"rain": "city-rain",
		}
		for name, id := range forced {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				cities := stored()
				err := NewService(cities, &stubObservations{}, &stubForecasts{}).DeleteMeCity(t.Context(), caller, id)

				require.ErrorIs(t, err, ErrForcedSubscription)
				var pub *internal.PublicError
				require.ErrorAs(t, err, &pub, "the caller needs to be told that removing the city is the way out")
				assert.Contains(t, pub.Details(), "remove the city")
				assert.Empty(t, cities.removed)
			})
		}
	})

	t.Run("another user's forced row is not found, not forced", func(t *testing.T) {
		t.Parallel()

		cities := stored()
		err := NewService(cities, &stubObservations{}, &stubForecasts{}).DeleteMeCity(t.Context(), caller, "city-other-thaw")

		require.ErrorIs(t, err, internal.ErrNotFound)
		require.NotErrorIs(t, err, ErrForcedSubscription,
			"answering 'that one is forced' would confirm both that the row exists and what kind it is")
	})

	t.Run("a store failure is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")

		lookupBroken := stored()
		lookupBroken.getErr = down
		require.ErrorIs(t, NewService(lookupBroken, &stubObservations{}, &stubForecasts{}).
			DeleteMeCity(t.Context(), caller, "city-1"), down)

		removeBroken := stored()
		removeBroken.removeErr = down
		require.ErrorIs(t, NewService(removeBroken, &stubObservations{}, &stubForecasts{}).
			DeleteMeCity(t.Context(), caller, "city-1"), down)
	})
}

func TestService_DeleteMeLocation(t *testing.T) {
	t.Parallel()

	const caller = "55"

	t.Run("asks the store to delete every row the caller holds there", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{}
		require.NoError(t, NewService(cities, &stubObservations{}, &stubForecasts{}).DeleteMeLocation(t.Context(), caller, "1234"))

		require.Len(t, cities.removedByLocation, 1)
		assert.Equal(t, domain.UserTypeTelegram, cities.removedByLocation[0].userType)
		assert.Equal(t, caller, cities.removedByLocation[0].userID,
			"the delete is scoped to the caller in the statement itself, which is the whole ownership check")
		assert.Equal(t, "1234", cities.removedByLocation[0].locationID)
	})

	t.Run("nothing matched is reported as not found", func(t *testing.T) {
		t.Parallel()

		cities := &stubCities{removeByLocationErr: internal.ErrNotFound}
		err := NewService(cities, &stubObservations{}, &stubForecasts{}).DeleteMeLocation(t.Context(), caller, "1234")

		require.ErrorIs(t, err, internal.ErrNotFound,
			"no such location for anyone and somebody else's location are one answer")
	})

	t.Run("a store failure is reported", func(t *testing.T) {
		t.Parallel()

		down := errors.New("db down")
		cities := &stubCities{removeByLocationErr: down}
		require.ErrorIs(t, NewService(cities, &stubObservations{}, &stubForecasts{}).
			DeleteMeLocation(t.Context(), caller, "1234"), down)
	})
}

func TestServiceObtainMeCurrentForecast(t *testing.T) {
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

	t.Run("attaches the stored outlook to its city", func(t *testing.T) {
		t.Parallel()
		cities := &stubCities{cities: []domain.WeatherUserCity{newCity("1234", "Almaty")}}
		forecasts := &stubForecasts{days: map[string][]domain.WeatherForecastDay{
			"1234": {{ForecastDate: "2026-08-21"}, {ForecastDate: "2026-08-22"}},
		}}

		got, err := NewService(cities, &stubObservations{}, forecasts).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Len(t, got[0].Forecast, 2)
		assert.Equal(t, "2026-08-21", got[0].Forecast[0].ForecastDate)
	})

	t.Run("a location with no outlook yet is carried with an empty one", func(t *testing.T) {
		t.Parallel()
		cities := &stubCities{cities: []domain.WeatherUserCity{newCity("1234", "Almaty")}}

		got, err := NewService(cities, &stubObservations{}, &stubForecasts{}).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Empty(t, got[0].Forecast, "a daily fetch lags a freshly added city; that is a state, not a failure")
	})

	t.Run("one read per distinct location, not one per subscription", func(t *testing.T) {
		t.Parallel()
		twoKinds := newCity("1234", "Almaty")
		twoKinds.NotifyKind = domain.WeatherNotifyAlertHeat
		cities := &stubCities{cities: []domain.WeatherUserCity{newCity("1234", "Almaty"), twoKinds}}
		forecasts := &stubForecasts{}

		got, err := NewService(cities, &stubObservations{}, forecasts).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, 1, forecasts.calls)
	})

	t.Run("an unloadable timezone costs the city its outlook, not its reading", func(t *testing.T) {
		t.Parallel()
		broken := newCity("1234", "Almaty")
		broken.Timezone = "Mars/Olympus_Mons"
		cities := &stubCities{cities: []domain.WeatherUserCity{broken}}
		forecasts := &stubForecasts{}

		got, err := NewService(cities, &stubObservations{}, forecasts).ObtainMeCurrent(t.Context(), "42")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Empty(t, got[0].Forecast)
		assert.Zero(t, forecasts.calls, "an outlook cannot be windowed without a local day to anchor it")
	})
}
