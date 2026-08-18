// Package weather is the application service behind a user's own weather
// subscriptions: the cities they track and the latest stored observation for
// each of them.
//
// It is free of HTTP concerns — rendering a city as JSON belongs to the gateway
// — and it does not talk to Open-Meteo. Observations arrive here the way the
// collector left them, from the store.
package weather

import (
	"context"
	"errors"
	"fmt"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// CurrentCity pairs one tracked city with its latest stored observation.
type CurrentCity struct {
	// City is the subscription row that contributed this location. A user
	// tracking one location under several notify kinds appears once, carrying
	// the first row the store returned for it.
	City domain.WeatherUserCity
	// Observation is the newest stored reading for City.LocationID, or nil when
	// the collector has not produced one yet. That is the normal state of a
	// just-added city, not a failure, and callers render it as "no data yet".
	Observation *domain.WeatherObservation
}

// Service reads a user's weather subscriptions and the observations collected
// for them. Construct it with NewService; it holds no mutable state and is safe
// for concurrent use.
type Service struct {
	cities CitiesLoader
	obs    ObservationsLoader
}

// NewService constructs a Service over the city-subscription and observation
// stores. In production both are repositories.
func NewService(cities CitiesLoader, obs ObservationsLoader) *Service {
	return &Service{cities: cities, obs: obs}
}

// ObtainMeCities returns every city subscription userID owns — one row per
// (location, notify kind) pair, so a city tracked for a morning summary and a
// heat alert appears twice.
func (s *Service) ObtainMeCities(ctx context.Context, userID string) ([]domain.WeatherUserCity, error) {
	return s.cities.ObtainWeatherUserCitiesByUserID(ctx, domain.UserTypeTelegram, userID)
}

// ObtainMeCurrent returns the latest stored observation for each distinct
// location userID tracks, deduplicated by location: notify kinds are a
// subscription concern, and a physical city has one set of readings regardless
// of how many alerts point at it.
//
// A location with no observation yet is returned with a nil Observation rather
// than dropped, so a just-added city is still listed while its first collection
// is pending.
func (s *Service) ObtainMeCurrent(ctx context.Context, userID string) ([]CurrentCity, error) {
	cities, err := s.cities.ObtainWeatherUserCitiesByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(cities))
	current := make([]CurrentCity, 0, len(cities))
	for _, city := range cities {
		if _, ok := seen[city.LocationID]; ok {
			continue
		}
		seen[city.LocationID] = struct{}{}

		obs, obsErr := s.obs.ObtainLatestObservation(ctx, city.LocationID, domain.ProviderOpenMeteo)
		if obsErr != nil {
			if !errors.Is(obsErr, internal.ErrNotFound) {
				return nil, fmt.Errorf("latest observation for %s: %w", city.LocationID, obsErr)
			}
			// Nothing collected for this location yet. Carry the city with no
			// reading rather than failing the whole list for one pending city.
			obs = nil
		}

		current = append(current, CurrentCity{City: city, Observation: obs})
	}

	return current, nil
}

// CitiesLoader reads the weather city subscriptions one user owns. Satisfied by
// *repository.WeatherUserCityRepository.
type CitiesLoader interface {
	ObtainWeatherUserCitiesByUserID(
		ctx context.Context, userType domain.UserType, userID string,
	) ([]domain.WeatherUserCity, error)
}

// ObservationsLoader reads the newest stored observation for one location.
// It reports internal.ErrNotFound when the location has never been collected;
// that is an expected answer here, not an error to propagate.
type ObservationsLoader interface {
	ObtainLatestObservation(ctx context.Context, locationID, provider string) (*domain.WeatherObservation, error)
}
