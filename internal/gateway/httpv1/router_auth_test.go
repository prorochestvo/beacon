package httpv1

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	appprofile "github.com/seilbekskindirov/beacon/internal/application/profile"
	appsub "github.com/seilbekskindirov/beacon/internal/application/subscription"
	appweather "github.com/seilbekskindirov/beacon/internal/application/weather"
	"github.com/seilbekskindirov/beacon/internal/domain"
	"github.com/seilbekskindirov/beacon/internal/dto"
	"github.com/seilbekskindirov/beacon/internal/gateway/httpv1/routes"
	"github.com/stretchr/testify/require"
)

var (
	_ weatherGeocoder               = (*stubGeocoder)(nil)
	_ appprofile.Store              = (*stubMeProfileRepo)(nil)
	_ appsub.SubscriptionsStore     = (*stubMeSubRepo)(nil)
	_ appsub.SourcesLoader          = (*stubMeSourceRepo)(nil)
	_ appsub.ValuesLoader           = (*stubMeRateValueRepo)(nil)
	_ appweather.CitiesStore        = (*stubWeatherCityRepo)(nil)
	_ appweather.ObservationsLoader = (*stubWeatherObsRepo)(nil)
)

// meRoutes is every method+path under /api/v1/me, the project's only authenticated
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

// authTestBotToken is the token the router under test verifies against, so
// signedInitData can mint a credential the real validator accepts.
const authTestBotToken = "12345:AAH-test-token"

// newAuthTestRouter builds the real router with dependencies that refuse to be used.
//
// The booby-trapped stubs are the assertion, not a shortcut: the /api/v1/me family is
// mounted behind the initData middleware, so an unauthenticated request must never
// reach a repository. A request that gets through anyway panics on its first
// repository call instead of quietly returning data — which the subtests below catch
// and report as the auth failure it is. The application services are the real ones,
// built over the same stubs, so the trap survives the layer they were moved behind.
func newAuthTestRouter(t *testing.T) http.Handler {
	t.Helper()
	mux, err := NewRouter(
		http.NewServeMux(),
		// A nil *service.RateRestApi: a typed nil, so any method call on it is a nil
		// dereference — the same trap the stubs set, without needing to restate the
		// whole rate-service surface here.
		nil,              // rate service
		authTestBotToken, // botToken
		appsub.NewService(stubMeSubRepo{}, stubMeSourceRepo{}, stubMeRateValueRepo{}),
		appprofile.NewService(stubMeProfileRepo{}),
		nil,        // chart service
		nil,        // health agent
		"test",     // server version
		time.Now(), // server start
		WeatherGatewayDeps{
			Service:  appweather.NewService(stubWeatherCityRepo{}, stubWeatherObsRepo{}),
			Geocoder: stubGeocoder{},
		},
	)
	require.NoError(t, err)
	return mux
}

// TestEveryMeRouteRejectsUnauthenticated is the guard against the failure mode that
// makes the duplicated auth check dangerous: a new /api/v1/me/* handler that forgets it
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
// /api/v1/me/* route and forgetting the auth check would also mean forgetting the row
// that would have caught it, and the guard would pass while covering nothing.
func TestMeRouteTableIsComplete(t *testing.T) {
	t.Parallel()

	// Constants are not reflectable, so the declaration source is the enumeration.
	body, err := os.ReadFile("routes/routes.go")
	require.NoError(t, err)

	declared := regexp.MustCompile(`"(/api/v1/me/[^"]*)"`).FindAllStringSubmatch(string(body), -1)
	require.NotEmpty(t, declared, "found no /api/v1/me/* constants to check against")

	covered := make(map[string]bool, len(meRoutes))
	for _, route := range meRoutes {
		covered[route.path] = true
	}

	var missing []string
	for _, match := range declared {
		path := match[1]
		// MePrefix is where the authenticated mux is mounted, not a route anyone can
		// call. It has no handler and therefore no row here; every real route below
		// it does.
		if path == routes.MePrefix {
			continue
		}
		concrete := regexp.MustCompile(`\{[a-z_]+\}`).ReplaceAllString(path, "abc")
		if !covered[concrete] {
			missing = append(missing, path)
		}
	}
	require.Emptyf(t, missing,
		"these /api/v1/me/* routes have no row in meRoutes, so nothing checks that they "+
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

// TestMeRoutesResolveThroughTheMount is the other half of the guard above: that one
// proves an unauthenticated caller is refused, this one proves a legitimate caller
// still arrives. Moving the family onto a sub-mux is a routing change as much as an
// auth change, and a wildcard or a precedence rule broken by the mount would show up
// as a 404 on a route that used to work — which the 401-only guard cannot see.
func TestMeRoutesResolveThroughTheMount(t *testing.T) {
	t.Parallel()

	for _, route := range meRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			t.Parallel()
			mux := newAuthTestRouter(t)

			req := httptest.NewRequest(route.method, route.path, nil)
			req.Header.Set("X-Telegram-Init-Data", signedInitData(t, authTestBotToken, 4242))
			rec := httptest.NewRecorder()

			// The stubs panic on first use, so a panic here means the request reached
			// its handler — which is exactly what this test wants to establish. Not
			// every handler touches a dependency before returning (a missing query
			// parameter answers 400 first), so the status is checked when it does not.
			reached := false
			func() {
				defer func() {
					if recover() != nil {
						reached = true
					}
				}()
				mux.ServeHTTP(rec, req)
			}()

			if reached {
				return
			}
			require.NotEqual(t, http.StatusNotFound, rec.Code,
				"%s %s did not resolve through the /api/v1/me/ mount", route.method, route.path)
			require.NotEqual(t, http.StatusUnauthorized, rec.Code,
				"%s %s rejected a validly signed credential", route.method, route.path)
		})
	}
}

func TestMeMountKeepsMethodMatching(t *testing.T) {
	t.Parallel()

	// A sub-mux mounted on a pattern that carries no method could plausibly swallow
	// method matching and answer 404 for a wrong verb. It must stay 405: the route
	// exists, the verb does not.
	mux := newAuthTestRouter(t)

	req := httptest.NewRequest(http.MethodPut, routes.MeSubscriptions, nil)
	req.Header.Set("X-Telegram-Init-Data", signedInitData(t, authTestBotToken, 4242))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code,
		"a wrong verb inside the family must be 405, not 404")
}

// signedInitData produces a correctly-signed initData string for userID. It mirrors
// the Telegram WebApp signing algorithm; tgwebapp's own test has the same helper, and
// duplicating twenty lines is cheaper than a shared test-fixture package.
func signedInitData(t *testing.T, botToken string, userID int64) string {
	t.Helper()

	fields := map[string]string{
		"user":      fmt.Sprintf(`{"id":%d,"first_name":"Test"}`, userID),
		"auth_date": fmt.Sprintf("%d", time.Now().Unix()),
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(fields[k])
	}

	secretMac := hmac.New(sha256.New, []byte("WebAppData"))
	secretMac.Write([]byte(botToken))
	dataMac := hmac.New(sha256.New, secretMac.Sum(nil))
	dataMac.Write([]byte(sb.String()))

	vals := url.Values{}
	for k, v := range fields {
		vals.Set(k, v)
	}
	vals.Set("hash", hex.EncodeToString(dataMac.Sum(nil)))
	return vals.Encode()
}
