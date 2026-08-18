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
	"strings"
	"time"

	"github.com/seilbekskindirov/beacon/internal"
	"github.com/seilbekskindirov/beacon/internal/domain"
)

// ErrForcedSubscription reports an attempt to remove a system-managed alert row
// on its own. Thaw and rain are forced per tracked city: they go when the city
// goes and not before, so this is a conflict with how the resource is shaped
// rather than a malformed request. It arrives joined with a
// *internal.PublicError carrying the explanation for the user.
var ErrForcedSubscription = errors.New("weather subscription is forced and system-managed")

// NewCity is a request to track one city under one notify kind. There is no user
// field: ownership comes from the authenticated caller passed alongside it.
//
// Every field is copied by the client out of a geocoding search result this
// service never saw, so all of them are validated here rather than trusted.
type NewCity struct {
	LocationID  string
	DisplayName string
	Latitude    float64
	Longitude   float64
	Timezone    string
	Country     string
	Admin1      string
	// NotifyKind defaults to domain.WeatherNotifyMorningSummary when empty.
	NotifyKind domain.WeatherNotifyKind
	// NotifyHour is the city-local hour a daily summary fires at. Nil takes the
	// default; a value outside [0,23] is refused.
	NotifyHour *int
	// ConditionValue is the alert threshold. The kinds that have no threshold
	// store it empty whatever the request said.
	ConditionValue string
}

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
	cities CitiesStore
	obs    ObservationsLoader
}

// NewService constructs a Service over the city-subscription and observation
// stores. In production both are repositories.
func NewService(cities CitiesStore, obs ObservationsLoader) *Service {
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

// CreateMeCity starts tracking a city for userID and returns the identifier of
// the row it created, then ensures the forced alert rows that city must carry.
//
// A request the caller can fix — a field missing, a coordinate off the globe, a
// timezone that will not load, a threshold the kind cannot use — comes back as a
// *internal.PublicError naming it.
func (s *Service) CreateMeCity(ctx context.Context, userID string, req NewCity) (string, error) {
	record, err := resolveNewCity(userID, req)
	if err != nil {
		return "", err
	}
	// RetainWeatherUserCity mutates record in place (identifier, timestamps), so
	// the requested kind is read before the write and the forced rows below are
	// built from req rather than from record.
	requested := record.NotifyKind

	if err := s.cities.RetainWeatherUserCity(ctx, record); err != nil {
		return "", fmt.Errorf("retain city: %w", err)
	}
	if err := s.ensureForcedKinds(ctx, userID, req, requested); err != nil {
		return "", err
	}
	return record.ID, nil
}

// DeleteMeCity removes the city subscription id, which userID must own,
// reporting internal.ErrNotFound when it does not exist or belongs to somebody
// else, and ErrForcedSubscription when it is one of the rows a tracked city must
// always carry.
//
// Ownership is settled first, so a forced row belonging to another user is still
// only ever "not found" — answering "that one is forced" would confirm it exists.
func (s *Service) DeleteMeCity(ctx context.Context, userID, id string) error {
	city, err := s.obtainOwnedCity(ctx, userID, id)
	if err != nil {
		return err
	}

	if notice, forced := forcedKindNotice(city.NotifyKind); forced {
		return errors.Join(ErrForcedSubscription, internal.NewPublicError(notice))
	}

	if err := s.cities.RemoveWeatherUserCity(ctx, city); err != nil {
		return fmt.Errorf("remove city: %w", err)
	}
	return nil
}

// DeleteMeLocation removes every subscription userID holds at locationID, forced
// rows included. Removing the location is the only way to turn those off, which
// is what DeleteMeCity refuses to do piecemeal.
//
// The store's atomic delete is the whole ownership check: it reports
// internal.ErrNotFound when no row matched, which covers "no such location for
// anyone" and "somebody else's location" as one answer.
func (s *Service) DeleteMeLocation(ctx context.Context, userID, locationID string) error {
	if err := s.cities.RemoveWeatherUserCitiesByLocation(ctx, domain.UserTypeTelegram, userID, locationID); err != nil {
		return fmt.Errorf("remove location: %w", err)
	}
	return nil
}

// ensureForcedKinds gives a tracked city the alert rows it must always carry,
// skipping whichever kind the request itself created.
//
// alert_thaw and rain_alert are forced and system-managed: every tracked city
// holds exactly one of each, so any add ensures the ones this request did not
// create. The rows are built from req, not from the record just written, which
// RetainWeatherUserCity mutated in place.
//
// The AlertLatched seed differs per kind, and the asymmetry is deliberate:
//
//   - thaw starts pre-latched (true), matching migration 202607.021: it fires on
//     TempMax > 0 alone, the default warm-season state, so an armed row would risk
//     firing on the very first check tick for a city already past freezing. A
//     genuinely still-frozen city re-arms to false on the next tick with no
//     notification either way.
//   - rain starts armed (false), matching migration 202608.026: it notifies on BOTH
//     latch edges, so a pre-latched row would emit a spurious "rain cleared" on the
//     first tick, "no rain" being the normal state. Armed, the only first-tick message
//     it can produce is a truthful "rain expected".
//
// keepExisting rows are ensured only when absent, so re-adding any other alert kind for
// the same city cannot stomp a rain threshold the user retuned (the upsert does rewrite
// condition_value).
func (s *Service) ensureForcedKinds(ctx context.Context, userID string, req NewCity, requested domain.WeatherNotifyKind) error {
	for _, forced := range forcedKinds {
		if requested == forced.kind {
			continue // the request itself created this row
		}
		if forced.keepExisting {
			exists, err := s.kindExists(ctx, userID, req.LocationID, forced.kind)
			if err != nil {
				return fmt.Errorf("ensure %s: %w", forced.kind, err)
			}
			if exists {
				continue
			}
		}
		row := &domain.WeatherUserCity{
			UserType:       domain.UserTypeTelegram,
			UserID:         userID,
			LocationID:     req.LocationID,
			DisplayName:    req.DisplayName,
			Latitude:       req.Latitude,
			Longitude:      req.Longitude,
			Timezone:       req.Timezone,
			Country:        req.Country,
			Admin1:         req.Admin1,
			NotifyKind:     forced.kind,
			NotifyHour:     defaultNotifyHour,
			ConditionValue: forced.conditionValue,
			AlertLatched:   forced.alertLatched,
		}
		if err := s.cities.RetainWeatherUserCity(ctx, row); err != nil {
			return fmt.Errorf("ensure %s: %w", forced.kind, err)
		}
	}
	return nil
}

// kindExists reports whether userID already owns a subscription of kind at
// locationID. Ensuring a forced kind that carries a user-tunable threshold has to
// be skipped when the row is already there, because RetainWeatherUserCity's
// upsert rewrites condition_value — without this check, adding any second alert
// to a city would silently reset a rain threshold the user had retuned.
func (s *Service) kindExists(ctx context.Context, userID, locationID string, kind domain.WeatherNotifyKind) (bool, error) {
	rows, err := s.cities.ObtainWeatherUserCitiesByUserID(ctx, domain.UserTypeTelegram, userID)
	if err != nil {
		return false, err
	}
	for i := range rows {
		if rows[i].LocationID == locationID && rows[i].NotifyKind == kind {
			return true, nil
		}
	}
	return false, nil
}

// obtainOwnedCity loads city subscription id and reports internal.ErrNotFound
// unless userID owns it.
//
// A row that does not exist and one belonging to somebody else are one sentinel
// on purpose, and the caller is given no way to tell them apart — see the same
// reasoning in the subscription service.
func (s *Service) obtainOwnedCity(ctx context.Context, userID, id string) (*domain.WeatherUserCity, error) {
	city, err := s.cities.ObtainWeatherUserCityByID(ctx, id)
	if err != nil && !errors.Is(err, internal.ErrNotFound) {
		return nil, fmt.Errorf("city lookup: %w", err)
	}
	if err != nil || city == nil || city.UserID != userID {
		return nil, internal.ErrNotFound
	}
	return city, nil
}

// CitiesStore reads and writes the weather city subscriptions one user owns.
// Satisfied by *repository.WeatherUserCityRepository.
//
// ObtainWeatherUserCityByID and RemoveWeatherUserCitiesByLocation both report
// internal.ErrNotFound when nothing matched.
type CitiesStore interface {
	ObtainWeatherUserCitiesByUserID(
		ctx context.Context, userType domain.UserType, userID string,
	) ([]domain.WeatherUserCity, error)
	ObtainWeatherUserCityByID(ctx context.Context, id string) (*domain.WeatherUserCity, error)
	RetainWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
	RemoveWeatherUserCity(ctx context.Context, record *domain.WeatherUserCity) error
	RemoveWeatherUserCitiesByLocation(ctx context.Context, userType domain.UserType, userID, locationID string) error
}

// ObservationsLoader reads the newest stored observation for one location.
// It reports internal.ErrNotFound when the location has never been collected;
// that is an expected answer here, not an error to propagate.
type ObservationsLoader interface {
	ObtainLatestObservation(ctx context.Context, locationID, provider string) (*domain.WeatherObservation, error)
}

const (
	// defaultNotifyHour is the city-local hour used when a request omits one.
	defaultNotifyHour = 7
	// defaultRainThreshold is the precipitation probability percent seeded into
	// the forced rain_alert row of a newly tracked city. It matches the value the
	// backfill migration (202608.026) writes for pre-existing cities. Users retune
	// it by re-adding a rain alert with a different threshold, which upserts
	// condition_value in place.
	defaultRainThreshold = "60"
)

// forcedKinds are the alert rows every tracked city carries. See
// ensureForcedKinds for why the latch seeds differ and why only one is kept
// when it already exists.
var forcedKinds = []struct {
	kind           domain.WeatherNotifyKind
	conditionValue string
	alertLatched   bool
	keepExisting   bool
}{
	{domain.WeatherNotifyAlertThaw, "", true, false},
	{domain.WeatherNotifyAlertRain, defaultRainThreshold, false, true},
}

// resolveNewCity validates req and turns it into the row to store, applying the
// defaults for an omitted notify kind and hour. Every rejection is a
// *internal.PublicError: they are all the caller's to fix, and naming them is
// the only way they can.
func resolveNewCity(userID string, req NewCity) (*domain.WeatherUserCity, error) {
	if strings.TrimSpace(req.LocationID) == "" {
		return nil, internal.NewPublicError("location_id is required")
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		return nil, internal.NewPublicError("display_name is required")
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		return nil, internal.NewPublicError("invalid timezone: must be a valid IANA timezone name")
	}
	if req.Latitude < -90 || req.Latitude > 90 {
		return nil, internal.NewPublicError("latitude must be between -90 and 90")
	}
	if req.Longitude < -180 || req.Longitude > 180 {
		return nil, internal.NewPublicError("longitude must be between -180 and 180")
	}

	notifyHour := defaultNotifyHour
	if req.NotifyHour != nil {
		notifyHour = *req.NotifyHour
	}
	if notifyHour < 0 || notifyHour > 23 {
		return nil, internal.NewPublicError("notify_hour must be between 0 and 23")
	}

	notifyKind := domain.WeatherNotifyMorningSummary
	if req.NotifyKind != "" {
		notifyKind = req.NotifyKind
	}

	// morning_summary and alert_thaw ignore condition_value; normalise to empty
	// so arbitrary free text is never stored for a kind that does not use it.
	// (alert_thunderstorm is not blanked here — pre-existing asymmetry.)
	conditionValue := req.ConditionValue
	if notifyKind == domain.WeatherNotifyMorningSummary || notifyKind == domain.WeatherNotifyAlertThaw {
		conditionValue = ""
	}

	record := &domain.WeatherUserCity{
		UserType:       domain.UserTypeTelegram,
		UserID:         userID,
		LocationID:     req.LocationID,
		DisplayName:    req.DisplayName,
		Latitude:       req.Latitude,
		Longitude:      req.Longitude,
		Timezone:       req.Timezone,
		Country:        req.Country,
		Admin1:         req.Admin1,
		NotifyKind:     notifyKind,
		NotifyHour:     notifyHour,
		ConditionValue: conditionValue,
	}
	// Validate judges the (kind, condition_value) pair. Its message is written
	// for a person and is safe to hand back unchanged.
	if err := record.Validate(); err != nil {
		return nil, internal.NewPublicError(err.Error())
	}
	return record, nil
}

// forcedKindNotice returns the explanation for a forced, system-managed kind
// that cannot be removed on its own, and ok=false for every kind a user may
// delete freely.
func forcedKindNotice(kind domain.WeatherNotifyKind) (notice string, ok bool) {
	switch kind {
	case domain.WeatherNotifyAlertThaw:
		return "Thaw alerts stay on for every tracked city; remove the city to turn it off.", true
	case domain.WeatherNotifyAlertRain:
		return "Rain alerts stay on for every tracked city; remove the city to turn it off.", true
	default:
		return "", false
	}
}
