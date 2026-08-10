package httpV1_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
	"github.com/seilbekskindirov/beacon/internal/gateway/httpV1"
	"github.com/seilbekskindirov/beacon/internal/gateway/httpV1/routes"
	"github.com/stretchr/testify/require"
)

// meRoutes is every method+path under /api/me, the project's only authenticated
// surface. TestEveryMeRouteRejectsUnauthenticated walks it; TestMeRouteTableIsComplete
// proves the table has not fallen behind the route constants.
var meRoutes = []struct {
	method string
	path   string
}{
	{http.MethodGet, routes.MeSubscriptions},
	{http.MethodPost, routes.MeSubscriptions},
	{http.MethodGet, routes.MeSubscriptionsRaw},
	{http.MethodPatch, strings.Replace(routes.MeSubscriptionByID, "{id}", "abc", 1)},
	{http.MethodDelete, strings.Replace(routes.MeSubscriptionByID, "{id}", "abc", 1)},
	{http.MethodGet, routes.MeRatesChart},
	{http.MethodGet, routes.MeRatesHistory},
	{http.MethodPost, routes.MeProfile},
	{http.MethodGet, routes.MeWeatherCurrent},
	{http.MethodGet, routes.MeWeatherCitiesSearch},
	{http.MethodGet, routes.MeWeatherCities},
	{http.MethodPost, routes.MeWeatherCities},
	{http.MethodDelete, strings.Replace(routes.MeWeatherCityByID, "{id}", "abc", 1)},
	{http.MethodDelete, strings.Replace(routes.MeWeatherLocationByID, "{location_id}", "abc", 1)},
}

// newAuthTestRouter builds the real router with dependencies that refuse to be used.
//
// The booby-trapped stubs are the assertion, not a shortcut: every /api/me/* handler
// authenticates before it touches a repository, so an unauthenticated request must
// never reach one. A handler that skipped the check panics on its first repository
// call instead of quietly returning data — which the subtests below catch and report
// as the auth failure it is.
func newAuthTestRouter(t *testing.T) http.Handler {
	t.Helper()
	mux, err := httpV1.NewRouter(
		http.NewServeMux(),
		// A nil *service.RateRestApi: a typed nil, so any method call on it is a nil
		// dereference — the same trap the stubs set, without needing to restate the
		// whole rate-service surface here.
		nil,              // rate service
		"test-bot-token", // botToken
		stubMeSubRepo{},
		stubMeSourceRepo{},
		stubMeRateValueRepo{},
		stubMeProfileRepo{},
		nil,        // chart service
		nil,        // health agent
		"test",     // server version
		time.Now(), // server start
		httpV1.WeatherGatewayDeps{
			CityRepo: stubWeatherCityRepo{},
			Geocoder: stubGeocoder{},
			ObsRepo:  stubWeatherObsRepo{},
		},
	)
	require.NoError(t, err)
	return mux
}

// TestEveryMeRouteRejectsUnauthenticated is the guard against the failure mode that
// makes the duplicated auth check dangerous: a new /api/me/* handler that forgets it
// serves another user's data with no error and no log line. Fourteen copies of the
// same four lines is the mechanism; nothing enforcing them is the risk.
func TestEveryMeRouteRejectsUnauthenticated(t *testing.T) {
	t.Parallel()

	headers := map[string]map[string]string{
		"no header":         nil,
		"empty header":      {"X-Telegram-Init-Data": ""},
		"garbage header":    {"X-Telegram-Init-Data": "not-signed-at-all"},
		"unsigned key pair": {"X-Telegram-Init-Data": "user=%7B%22id%22%3A1%7D&auth_date=1"},
	}

	for _, route := range meRoutes {
		for name, header := range headers {
			t.Run(route.method+" "+route.path+" / "+name, func(t *testing.T) {
				t.Parallel()
				mux := newAuthTestRouter(t)

				req := httptest.NewRequest(route.method, route.path, nil)
				for k, v := range header {
					req.Header.Set(k, v)
				}
				rec := httptest.NewRecorder()

				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("handler panicked reaching a dependency (%v) — the auth "+
							"check did not run first", rec)
					}
				}()
				mux.ServeHTTP(rec, req)

				require.Equalf(t, http.StatusUnauthorized, rec.Code,
					"%s %s must reject an unauthenticated caller", route.method, route.path)
			})
		}
	}
}

// TestMeRouteTableIsComplete keeps the table above honest. Without it, adding an
// /api/me/* route and forgetting the auth check would also mean forgetting the row
// that would have caught it, and the guard would pass while covering nothing.
func TestMeRouteTableIsComplete(t *testing.T) {
	t.Parallel()

	// Constants are not reflectable, so the declaration source is the enumeration.
	body, err := os.ReadFile("routes/routes.go")
	require.NoError(t, err)

	declared := regexp.MustCompile(`"(/api/me/[^"]*)"`).FindAllStringSubmatch(string(body), -1)
	require.NotEmpty(t, declared, "found no /api/me/* constants to check against")

	covered := make(map[string]bool, len(meRoutes))
	for _, route := range meRoutes {
		covered[route.path] = true
	}

	var missing []string
	for _, match := range declared {
		path := match[1]
		concrete := regexp.MustCompile(`\{[a-z_]+\}`).ReplaceAllString(path, "abc")
		if !covered[concrete] {
			missing = append(missing, path)
		}
	}
	require.Emptyf(t, missing,
		"these /api/me/* routes have no row in meRoutes, so nothing checks that they "+
			"require authentication:\n  %s", strings.Join(missing, "\n  "))
}

// The stubs below satisfy NewHandler, which refuses a Config missing any required
// dependency. Every method panics: reaching one would mean the auth check did not run
// first, and the subtests report that as the failure it is. A panicking stub is a
// sharper trap than an untyped nil — it names what went wrong instead of surfacing an
// anonymous nil dereference.

type stubMeSubRepo struct{}

func (stubMeSubRepo) ObtainRateUserSubscriptionsByUserID(context.Context, domain.UserType, string) ([]domain.RateUserSubscription, error) {
	panic("reached the repository without authenticating")
}
func (stubMeSubRepo) ObtainRateUserSubscriptionByID(context.Context, string) (*domain.RateUserSubscription, error) {
	panic("reached the repository without authenticating")
}
func (stubMeSubRepo) RetainRateUserSubscription(context.Context, *domain.RateUserSubscription) error {
	panic("reached the repository without authenticating")
}
func (stubMeSubRepo) RemoveRateUserSubscription(context.Context, *domain.RateUserSubscription) error {
	panic("reached the repository without authenticating")
}

type stubMeSourceRepo struct{}

func (stubMeSourceRepo) ObtainRateSourceByName(context.Context, string) (*domain.RateSource, error) {
	panic("reached the repository without authenticating")
}
func (stubMeSourceRepo) ObtainRateSourcesByNames(context.Context, []string) (map[string]domain.RateSource, error) {
	panic("reached the repository without authenticating")
}

type stubMeRateValueRepo struct{}

func (stubMeRateValueRepo) ObtainLastNRateValuesBySourceName(context.Context, string, int64) ([]domain.RateValue, error) {
	panic("reached the repository without authenticating")
}
func (stubMeRateValueRepo) ObtainLatestRateValuesBySourceNames(context.Context, []string) (map[string]domain.RateValue, error) {
	panic("reached the repository without authenticating")
}

type stubMeProfileRepo struct{}

func (stubMeProfileRepo) UpsertRateUserProfile(context.Context, *domain.RateUserProfile) error {
	panic("reached the repository without authenticating")
}

type stubWeatherCityRepo struct{}

func (stubWeatherCityRepo) RetainWeatherUserCity(context.Context, *domain.WeatherUserCity) error {
	panic("reached the repository without authenticating")
}
func (stubWeatherCityRepo) ObtainWeatherUserCitiesByUserID(context.Context, domain.UserType, string) ([]domain.WeatherUserCity, error) {
	panic("reached the repository without authenticating")
}
func (stubWeatherCityRepo) ObtainWeatherUserCityByID(context.Context, string) (*domain.WeatherUserCity, error) {
	panic("reached the repository without authenticating")
}
func (stubWeatherCityRepo) RemoveWeatherUserCity(context.Context, *domain.WeatherUserCity) error {
	panic("reached the repository without authenticating")
}
func (stubWeatherCityRepo) RemoveWeatherUserCitiesByLocation(context.Context, domain.UserType, string, string) error {
	panic("reached the repository without authenticating")
}

type stubGeocoder struct{}

func (stubGeocoder) Geocode(context.Context, string, int) ([]dto.WeatherCitySearchItem, error) {
	panic("reached the geocoder without authenticating")
}

type stubWeatherObsRepo struct{}

func (stubWeatherObsRepo) ObtainLatestObservation(context.Context, string, string) (*domain.WeatherObservation, error) {
	panic("reached the repository without authenticating")
}
