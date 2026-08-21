package collection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/prorochestvo/loginjector"
	"github.com/seilbekskindirov/beacon/internal/domain"
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
	logger   io.Writer
}

// NewWeatherForecastAgent constructs a WeatherForecastAgent. provider, cityRepo and dayRepo
// are all required; a nil logger discards output.
func NewWeatherForecastAgent(
	provider weatherRangeProvider,
	cityRepo weatherCollectionCityRepo,
	dayRepo weatherForecastDayRepo,
	logger io.Writer,
) (*WeatherForecastAgent, error) {
	if provider == nil || cityRepo == nil || dayRepo == nil {
		return nil, errors.New("weather forecast agent: provider, cityRepo, and dayRepo are all required")
	}
	if logger == nil {
		logger = io.Discard
	}
	return &WeatherForecastAgent{
		provider: provider,
		cityRepo: cityRepo,
		dayRepo:  dayRepo,
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
	var fetched, skipped, failed int
	total := len(locations)

	for _, loc := range locations {
		if !a.isDue(ctx, loc.LocationID, now) {
			skipped++
			continue
		}

		days, fetchErr := a.provider.ForecastRange(ctx, loc.Latitude, loc.Longitude)
		if fetchErr != nil {
			failed++
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
			fmt.Fprintf(a.logger, "weather forecast: location %s: retain error: %v\n", loc.LocationID, retainErr)
			errs = append(errs, fmt.Errorf("location %s: retain forecast: %w", loc.LocationID, retainErr))
			continue
		}
		fetched++
	}

	fmt.Fprintf(a.logger, "weather forecast: fetched=%d skipped=%d failed=%d total=%d\n", fetched, skipped, failed, total)

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

// isDue reports whether the location still needs its fetch for the current UTC day.
//
// The gate is a calendar-day comparison rather than "at least 24 h since the last capture".
// Against an hourly cron the elapsed-time form drifts an hour later every day and eventually
// lands after the subscriber's notify hour, so the digest would read a forecast a day older
// than it needed to be; a calendar day pins the fetch to the first tick after midnight UTC
// and stays there.
//
// A read failure counts as due. ErrNotFound means the location has never been fetched, and
// anything else must not be allowed to skip a location permanently.
func (a *WeatherForecastAgent) isDue(ctx context.Context, locationID string, now time.Time) bool {
	last, err := a.dayRepo.ObtainLatestForecastCapture(ctx, locationID, domain.ProviderOpenMeteo)
	if err != nil {
		return true
	}
	return last.UTC().Format(time.DateOnly) != now.Format(time.DateOnly)
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
