package collection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/repository"
)

// WeatherForecastAgent collects the multi-week daily forecast from Open-Meteo for every
// distinct subscribed location and stores it in weather_forecast_days. Each Run invocation
// is one-shot, called once per cron tick from cmd/collector, and fetches each location at
// most once per UTC calendar day.
//
// It is deliberately not part of WeatherAgent. That agent answers "what is it doing right
// now", on an hourly throttle, and feeds the morning summary and the same-day alerts; this
// one answers "what will the next two weeks look like", where the upstream model is
// re-issued a few times a day and an hourly fetch would return unchanged bytes. Folding the
// two together would put the current-conditions path at risk for no gain.
type WeatherForecastAgent struct {
	provider weatherRangeProvider
	cityRepo weatherCollectionCityRepo
	dayRepo  weatherForecastDayRepo
	meta     metaRepository
	logger   io.Writer
}

// NewWeatherForecastAgent constructs a WeatherForecastAgent. provider, cityRepo, dayRepo
// and meta are all required; a nil logger discards output.
func NewWeatherForecastAgent(
	provider weatherRangeProvider,
	cityRepo weatherCollectionCityRepo,
	dayRepo weatherForecastDayRepo,
	meta metaRepository,
	logger io.Writer,
) (*WeatherForecastAgent, error) {
	if provider == nil || cityRepo == nil || dayRepo == nil || meta == nil {
		return nil, errors.New("weather forecast agent: provider, cityRepo, dayRepo, and meta are all required")
	}
	if logger == nil {
		logger = io.Discard
	}
	return &WeatherForecastAgent{
		provider: provider,
		cityRepo: cityRepo,
		dayRepo:  dayRepo,
		meta:     meta,
		logger:   logger,
	}, nil
}

// Run fetches a fresh long-range forecast for every location that has not been fetched yet
// today and upserts it. One failing location never aborts the rest; the joined error names
// each one. Retention runs afterwards whatever happened above, since it is the only thing
// bounding the table and a fetch failure is no reason to keep yesterday's rows.
func (a *WeatherForecastAgent) Run(ctx context.Context) error {
	locations, err := a.cityRepo.ObtainDistinctWeatherLocations(ctx)
	if err != nil {
		return errors.Join(err, loginjector.NewTraceError())
	}

	now := time.Now().UTC()
	var errs []error
	var fetched, skipped, deferred, failed int
	total := len(locations)

	for _, loc := range locations {
		switch a.dueness(ctx, loc.LocationID, now) {
		case forecastFetched:
			skipped++
			continue
		case forecastBackingOff:
			// Counted apart from skipped on purpose. "Already have today's" and "failing and
			// waiting out its backoff" are opposite states, and a log that spells both
			// skipped is the log that made this bug invisible.
			deferred++
			continue
		case forecastDue:
		}

		days, fetchErr := a.provider.ForecastRange(ctx, loc.Latitude, loc.Longitude)
		if fetchErr != nil {
			failed++
			a.recordFailedAttempt(ctx, loc.LocationID, now)
			fmt.Fprintf(a.logger, "weather forecast: location %s: fetch error: %v\n", loc.LocationID, fetchErr)
			errs = append(errs, fmt.Errorf("location %s: forecast range: %w", loc.LocationID, fetchErr))
			continue
		}
		for i := range days {
			days[i].LocationID = loc.LocationID
		}

		// Persist under context.Background() so a SIGTERM does not discard a forecast that
		// was already fetched and paid for; the same reasoning as WeatherAgent's.
		//nolint:contextcheck // the detached context is the point; see the comment above
		if retainErr := a.dayRepo.RetainWeatherForecastDays(context.Background(), days); retainErr != nil {
			failed++
			a.recordFailedAttempt(ctx, loc.LocationID, now)
			fmt.Fprintf(a.logger, "weather forecast: location %s: retain error: %v\n", loc.LocationID, retainErr)
			errs = append(errs, fmt.Errorf("location %s: retain forecast: %w", loc.LocationID, retainErr))
			continue
		}
		fetched++
	}

	fmt.Fprintf(a.logger, "weather forecast: fetched=%d skipped=%d deferred=%d failed=%d total=%d\n",
		fetched, skipped, deferred, failed, total)

	// A day is kept until it is behind every subscriber, not merely behind UTC. Offsets run
	// from -12 to +14, so one whole day of slack is what makes "past" unambiguous; the cost
	// of the slack is one extra row per location.
	//nolint:contextcheck // detached for the same reason the write above is
	if pruneErr := a.dayRepo.RemoveForecastDaysBefore(context.Background(), now.AddDate(0, 0, -1).Format(time.DateOnly)); pruneErr != nil {
		fmt.Fprintf(a.logger, "weather forecast: retention: %v\n", pruneErr)
		errs = append(errs, fmt.Errorf("forecast retention: %w", pruneErr))
	}

	return errors.Join(errs...)
}

// forecastDueness is what one location owes the current tick.
type forecastDueness uint8

const (
	// forecastDue means the location should be fetched now.
	forecastDue forecastDueness = iota
	// forecastFetched means today's forecast is already stored.
	forecastFetched
	// forecastBackingOff means today's fetch has failed and the location is waiting out its
	// backoff, or has spent the day's budget of attempts.
	forecastBackingOff
)

// weatherForecastMaxDailyAttempts caps how many times one location is fetched in a UTC day
// while it keeps failing. Each attempt is itself up to openMeteoMaxAttempts HTTP requests, so
// five here is at most twenty-five requests a day for a location that never answers, against
// the twenty-four ticks times five that an ungated retry costs.
const weatherForecastMaxDailyAttempts = 5

// weatherForecastRetryBase is the wait after the first failed fetch of the day; each further
// failure doubles it. With the cap above, attempts land at roughly 0, 1, 3, 7 and 15 hours
// in — spread across the day rather than burned in the first five ticks, which matters
// because the outages measured against this provider run about three hours per location.
//
// To make retries denser or sparser, move this constant; to change how many there are, move
// the cap. Nothing else reads either.
const weatherForecastRetryBase = time.Hour

// dueness reports what the location owes this tick.
//
// The primary gate is a calendar-day comparison rather than "at least 24 h since the last
// capture". Against an hourly cron the elapsed-time form drifts an hour later every day and
// eventually lands after the subscriber's notify hour, so the digest would read a forecast a
// day older than it needed to be; a calendar day pins the fetch to the first tick after
// midnight UTC and stays there.
//
// A capture read that fails is not treated as "already fetched" — ErrNotFound means the
// location has never been fetched, and a storage fault must not skip a location permanently.
// It does still fall through to the attempt budget, because a persistent read fault would
// otherwise convert a storage problem into upstream traffic.
func (a *WeatherForecastAgent) dueness(ctx context.Context, locationID string, now time.Time) forecastDueness {
	last, err := a.dayRepo.ObtainLatestForecastCapture(ctx, locationID, domain.ProviderOpenMeteo)
	if err == nil && last.UTC().Format(time.DateOnly) == now.Format(time.DateOnly) {
		return forecastFetched
	}
	if !a.retryWindowOpen(ctx, locationID, now) {
		return forecastBackingOff
	}
	return forecastDue
}

// retryWindowOpen reports whether the location's attempt budget and backoff allow a fetch now.
//
// Every uncertainty resolves to true. A marker that cannot be read, cannot be parsed, or
// belongs to another day must not be what stops collection: the worst case of a wrong "true"
// is one extra request, and the worst case of a wrong "false" is a location that silently
// never updates.
func (a *WeatherForecastAgent) retryWindowOpen(ctx context.Context, locationID string, now time.Time) bool {
	raw, ok, err := a.meta.ObtainServiceMeta(ctx, forecastAttemptKey(locationID))
	if err != nil || !ok {
		return true
	}

	marker, parseErr := parseForecastAttempt(raw)
	if parseErr != nil || marker.day != now.Format(time.DateOnly) {
		return true
	}
	if marker.count >= weatherForecastMaxDailyAttempts {
		return false
	}
	return !now.Before(marker.lastAt.Add(forecastRetryWait(marker.count)))
}

// recordFailedAttempt increments the location's attempt count for today. A failure to write
// the marker is logged and swallowed: the cost is that this location keeps the old
// every-tick behaviour until the write succeeds, which is strictly better than losing the
// fetch itself to a bookkeeping error.
func (a *WeatherForecastAgent) recordFailedAttempt(ctx context.Context, locationID string, now time.Time) {
	today := now.Format(time.DateOnly)

	count := 0
	if raw, ok, err := a.meta.ObtainServiceMeta(ctx, forecastAttemptKey(locationID)); err == nil && ok {
		if marker, parseErr := parseForecastAttempt(raw); parseErr == nil && marker.day == today {
			count = marker.count
		}
	}

	value := formatForecastAttempt(forecastAttemptMarker{day: today, count: count + 1, lastAt: now})
	if err := a.meta.RetainServiceMeta(ctx, forecastAttemptKey(locationID), value); err != nil {
		fmt.Fprintf(a.logger, "weather forecast: location %s: record attempt: %v\n", locationID, err)
	}
}

// forecastRetryWait returns how long to wait after the given number of failures today.
func forecastRetryWait(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	return weatherForecastRetryBase << (failures - 1)
}

// forecastAttemptKey is the service_meta key holding one location's attempts for today. The
// row outlives a location that stops being subscribed; at a few dozen bytes each that is
// cheaper to leave than to reconcile.
func forecastAttemptKey(locationID string) string {
	return repository.ServiceMetaKeyForecastAttemptPrefix + locationID
}

// forecastAttemptMarker is one location's failed-fetch state for a single UTC day.
type forecastAttemptMarker struct {
	day    string // YYYY-MM-DD, UTC — a marker from another day is a fresh budget
	count  int
	lastAt time.Time
}

// formatForecastAttempt encodes a marker as "<YYYY-MM-DD>|<count>|<RFC3339>".
func formatForecastAttempt(m forecastAttemptMarker) string {
	return fmt.Sprintf("%s|%d|%s", m.day, m.count, m.lastAt.UTC().Format(time.RFC3339))
}

// parseForecastAttempt decodes what formatForecastAttempt wrote. Every caller treats an
// error as "no usable marker" rather than as a failure.
func parseForecastAttempt(raw string) (forecastAttemptMarker, error) {
	parts := strings.Split(raw, "|")
	if len(parts) != 3 {
		return forecastAttemptMarker{}, fmt.Errorf("forecast attempt marker: want 3 fields, got %d", len(parts))
	}

	count, err := strconv.Atoi(parts[1])
	if err != nil {
		return forecastAttemptMarker{}, fmt.Errorf("forecast attempt marker: count: %w", err)
	}
	lastAt, err := time.Parse(time.RFC3339, parts[2])
	if err != nil {
		return forecastAttemptMarker{}, fmt.Errorf("forecast attempt marker: last attempt: %w", err)
	}
	return forecastAttemptMarker{day: parts[0], count: count, lastAt: lastAt}, nil
}

// weatherRangeProvider fetches a multi-week daily forecast for the given coordinates.
type weatherRangeProvider interface {
	ForecastRange(ctx context.Context, lat, lng float64) ([]domain.WeatherForecastDay, error)
}

// weatherForecastDayRepo is the narrow forecast-table surface the collector needs.
type weatherForecastDayRepo interface {
	ObtainLatestForecastCapture(ctx context.Context, locationID, provider string) (time.Time, error)
	RemoveForecastDaysBefore(ctx context.Context, date string) error
	RetainWeatherForecastDays(ctx context.Context, records []domain.WeatherForecastDay) error
}
