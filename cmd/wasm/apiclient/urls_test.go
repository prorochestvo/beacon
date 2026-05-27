package apiclient

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseQuery is a test helper that splits rawURL on "?" and parses the query string.
func parseQuery(t *testing.T, rawURL string) (path string, vals url.Values) {
	t.Helper()
	parts := strings.SplitN(rawURL, "?", 2)
	require.Len(t, parts, 2, "expected URL to have a query string")
	parsed, err := url.ParseQuery(parts[1])
	require.NoError(t, err)
	return parts[0], parsed
}

func TestSourcesURL(t *testing.T) {
	t.Parallel()

	t.Run("encodes limit as query param", func(t *testing.T) {
		t.Parallel()
		path, q := parseQuery(t, sourcesURL(100))
		assert.Equal(t, "/api/sources", path)
		assert.Equal(t, "100", q.Get("limit"))
	})
}

func TestRatesURL(t *testing.T) {
	t.Parallel()

	t.Run("encodes name as path segment and limit as query param", func(t *testing.T) {
		t.Parallel()
		path, q := parseQuery(t, ratesURL("usd-eur", 50))
		assert.Equal(t, "/api/sources/usd-eur/rates", path)
		assert.Equal(t, "50", q.Get("limit"))
	})

	t.Run("path-escapes name containing slash", func(t *testing.T) {
		t.Parallel()
		raw := ratesURL("a/b", 10)
		// url.PathEscape encodes "/" as "%2F"
		assert.Contains(t, raw, "/api/sources/a%2Fb/rates")
	})

	t.Run("path-escapes name containing percent", func(t *testing.T) {
		t.Parallel()
		raw := ratesURL("100%", 10)
		assert.Contains(t, raw, "/api/sources/100%25/rates")
	})
}

func TestSubscriptionsURL(t *testing.T) {
	t.Parallel()

	t.Run("encodes name and page", func(t *testing.T) {
		t.Parallel()
		path, q := parseQuery(t, subscriptionsURL("btc-usd", 3))
		assert.Equal(t, "/api/sources/btc-usd/subscriptions/list", path)
		assert.Equal(t, "3", q.Get("page"))
	})

	t.Run("path-escapes name containing slash", func(t *testing.T) {
		t.Parallel()
		raw := subscriptionsURL("a/b", 1)
		assert.Contains(t, raw, "/api/sources/a%2Fb/subscriptions/list")
	})
}

func TestDailyEventsURL(t *testing.T) {
	t.Parallel()

	t.Run("encodes name and page", func(t *testing.T) {
		t.Parallel()
		path, q := parseQuery(t, dailyEventsURL("eth-usd", 2))
		assert.Equal(t, "/api/sources/eth-usd/events/daily", path)
		assert.Equal(t, "2", q.Get("page"))
	})

	t.Run("path-escapes name containing slash", func(t *testing.T) {
		t.Parallel()
		raw := dailyEventsURL("a/b", 1)
		assert.Contains(t, raw, "/api/sources/a%2Fb/events/daily")
	})
}

func TestExecutionErrorsURL(t *testing.T) {
	t.Parallel()

	t.Run("encodes page as query param", func(t *testing.T) {
		t.Parallel()
		path, q := parseQuery(t, executionErrorsURL(4))
		assert.Equal(t, "/api/errors/execution", path)
		assert.Equal(t, "4", q.Get("page"))
	})
}

func TestFailedNotificationsURL(t *testing.T) {
	t.Parallel()

	t.Run("encodes offset and limit as query params", func(t *testing.T) {
		t.Parallel()
		// index.html:333 uses ?offset={(page-1)*50}&limit=50
		path, q := parseQuery(t, failedNotificationsURL(50, 50))
		assert.Equal(t, "/api/notifications/failed", path)
		assert.Equal(t, "50", q.Get("offset"))
		assert.Equal(t, "50", q.Get("limit"))
	})

	t.Run("offset zero is encoded explicitly", func(t *testing.T) {
		t.Parallel()
		_, q := parseQuery(t, failedNotificationsURL(0, 50))
		assert.Equal(t, "0", q.Get("offset"))
		assert.Equal(t, "50", q.Get("limit"))
	})
}

func TestStatsURL(t *testing.T) {
	t.Parallel()

	t.Run("returns correct path with no query string", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "/api/stats", statsURL())
	})
}

func TestSourceActiveURL(t *testing.T) {
	t.Parallel()

	t.Run("encodes name as path segment", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "/api/sources/my-source/active", sourceActiveURL("my-source"))
	})

	t.Run("path-escapes name containing slash", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "/api/sources/a%2Fb/active", sourceActiveURL("a/b"))
	})
}

func TestMeSubscriptionsURL(t *testing.T) {
	t.Parallel()

	t.Run("encodes page and page_size", func(t *testing.T) {
		t.Parallel()
		path, q := parseQuery(t, meSubscriptionsURL(1, 10, ""))
		assert.Equal(t, "/api/me/subscriptions", path)
		assert.Equal(t, "1", q.Get("page"))
		assert.Equal(t, "10", q.Get("page_size"))
		assert.Equal(t, "", q.Get("q"), "q should be absent when empty")
	})

	t.Run("includes q when non-empty", func(t *testing.T) {
		t.Parallel()
		_, q := parseQuery(t, meSubscriptionsURL(2, 10, "USD"))
		assert.Equal(t, "USD", q.Get("q"))
	})

	t.Run("q param encodes special characters via url.Values", func(t *testing.T) {
		t.Parallel()
		raw := meSubscriptionsURL(1, 10, "a&b=c")
		// url.Values.Encode percent-encodes & and =
		assert.Contains(t, raw, "q=a%26b%3Dc")
	})
}

func TestRatesChartURL(t *testing.T) {
	t.Parallel()

	t.Run("happy path with week period", func(t *testing.T) {
		t.Parallel()
		path, q := parseQuery(t, chartURL("usd-eur", "week"))
		assert.Equal(t, "/api/sources/usd-eur/rates/chart", path)
		assert.Equal(t, "week", q.Get("period"))
	})

	t.Run("month period", func(t *testing.T) {
		t.Parallel()
		path, q := parseQuery(t, chartURL("usd-eur", "month"))
		assert.Equal(t, "/api/sources/usd-eur/rates/chart", path)
		assert.Equal(t, "month", q.Get("period"))
	})

	t.Run("year period", func(t *testing.T) {
		t.Parallel()
		path, q := parseQuery(t, chartURL("usd-eur", "year"))
		assert.Equal(t, "/api/sources/usd-eur/rates/chart", path)
		assert.Equal(t, "year", q.Get("period"))
	})

	t.Run("empty period omits query string entirely", func(t *testing.T) {
		t.Parallel()
		raw := chartURL("usd-eur", "")
		assert.Equal(t, "/api/sources/usd-eur/rates/chart", raw)
		assert.NotContains(t, raw, "?")
	})

	t.Run("name requiring path escape is encoded", func(t *testing.T) {
		t.Parallel()
		raw := chartURL("a/b c", "week")
		assert.Contains(t, raw, "/api/sources/a%2Fb%20c/rates/chart")
	})
}

func TestMeSubscriptionsHeaders(t *testing.T) {
	t.Parallel()

	t.Run("sets X-Telegram-Init-Data from parameter", func(t *testing.T) {
		t.Parallel()
		const token = "query_id=AAH&user=%7B%22id%22%3A123%7D"
		h := meSubscriptionsHeaders(token)
		assert.Equal(t, token, h["X-Telegram-Init-Data"])
	})

	t.Run("empty initData produces empty header value", func(t *testing.T) {
		t.Parallel()
		h := meSubscriptionsHeaders("")
		assert.Equal(t, "", h["X-Telegram-Init-Data"])
	})
}
