package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramInitData(t *testing.T) {
	t.Parallel()

	const callerUserID = int64(77)

	// served records whether the wrapped handler ran, and what it saw on the context.
	// "Did the inner handler run at all" is the assertion that matters on the reject
	// paths: a middleware that answered 401 and still called through would look
	// correct from the response alone.
	served := func(seen *int64, ran *bool) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*ran = true
			if id, ok := UserIDFrom(r.Context()); ok {
				*seen = id
			}
			w.WriteHeader(http.StatusOK)
		})
	}

	accept := func(_, _ string, _ time.Duration, _ time.Time) (int64, error) {
		return callerUserID, nil
	}
	reject := func(_, _ string, _ time.Duration, _ time.Time) (int64, error) {
		return 0, errors.New("bad signature")
	}

	baseConfig := func(validate func(string, string, time.Duration, time.Time) (int64, error)) TelegramInitDataConfig {
		return TelegramInitDataConfig{
			BotToken: "test-token",
			MaxAge:   24 * time.Hour,
			Validate: validate,
			Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		}
	}

	t.Run("a valid credential reaches the handler with the caller id", func(t *testing.T) {
		t.Parallel()

		var seen int64
		var ran bool
		h := TelegramInitData(baseConfig(accept))(served(&seen, &ran))

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil)
		req.Header.Set(InitDataHeader, "signed")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		require.Equal(t, http.StatusOK, rr.Code)
		assert.True(t, ran, "the wrapped handler must run")
		assert.Equal(t, callerUserID, seen, "the handler must see the validated caller id")
	})

	t.Run("a rejected credential 401s and the handler never runs", func(t *testing.T) {
		t.Parallel()

		headers := map[string]map[string]string{
			"no header":         nil,
			"empty header":      {InitDataHeader: ""},
			"garbage header":    {InitDataHeader: "not-signed-at-all"},
			"unsigned key pair": {InitDataHeader: "user=%7B%22id%22%3A1%7D&auth_date=1"},
		}
		for name, header := range headers {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				var seen int64
				var ran bool
				h := TelegramInitData(baseConfig(reject))(served(&seen, &ran))

				req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil)
				for k, v := range header {
					req.Header.Set(k, v)
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)

				require.Equal(t, http.StatusUnauthorized, rr.Code)
				assert.False(t, ran, "an unauthenticated request must not reach the handler")
				assert.JSONEq(t, `{"error":"unauthorized"}`, rr.Body.String(),
					"the body is part of the Mini App contract")
			})
		}
	})

	t.Run("the reason for a rejection is not disclosed", func(t *testing.T) {
		t.Parallel()

		var seen int64
		var ran bool
		detailed := func(_, _ string, _ time.Duration, _ time.Time) (int64, error) {
			return 0, errors.New("hash mismatch: expected abc123 got def456")
		}
		h := TelegramInitData(baseConfig(detailed))(served(&seen, &ran))

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil)
		req.Header.Set(InitDataHeader, "forged")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		// Echoing which part of a forged payload failed tells a prober how to forge
		// the next one.
		assert.NotContains(t, rr.Body.String(), "hash mismatch")
		assert.NotContains(t, rr.Body.String(), "abc123")
	})

	t.Run("the credential is read from the header, never the query string", func(t *testing.T) {
		t.Parallel()

		var got string
		capture := func(initData, _ string, _ time.Duration, _ time.Time) (int64, error) {
			got = initData
			return callerUserID, nil
		}

		var seen int64
		var ran bool
		h := TelegramInitData(baseConfig(capture))(served(&seen, &ran))

		// A signed payload in the URL would land in access logs and Referer headers
		// for the whole validity window, so the query string must be ignored even
		// when the header is absent.
		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions?initData=from-query", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		assert.Empty(t, got, "the query string must not be accepted as a credential")
		assert.Equal(t, http.StatusOK, rr.Code, "this stub accepts anything; the assertion above is the point")
	})

	t.Run("the configured clock is what ages the payload", func(t *testing.T) {
		t.Parallel()

		frozen := time.Unix(1_700_000_000, 0)
		var gotNow time.Time
		var gotMaxAge time.Duration
		capture := func(_, _ string, maxAge time.Duration, now time.Time) (int64, error) {
			gotNow, gotMaxAge = now, maxAge
			return callerUserID, nil
		}

		cfg := baseConfig(capture)
		cfg.MaxAge = 90 * time.Minute
		cfg.Now = func() time.Time { return frozen }

		var seen int64
		var ran bool
		h := TelegramInitData(cfg)(served(&seen, &ran))

		req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil)
		req.Header.Set(InitDataHeader, "signed")
		h.ServeHTTP(httptest.NewRecorder(), req)

		assert.Equal(t, frozen, gotNow, "expiry must be measured against the injected clock")
		assert.Equal(t, 90*time.Minute, gotMaxAge)
	})

	t.Run("a non-positive MaxAge is a wiring fault, not a failed login", func(t *testing.T) {
		t.Parallel()

		for name, maxAge := range map[string]time.Duration{"zero": 0, "negative": -time.Second} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				var seen int64
				var ran bool
				cfg := baseConfig(accept)
				cfg.MaxAge = maxAge
				h := TelegramInitData(cfg)(served(&seen, &ran))

				req := httptest.NewRequest(http.MethodGet, "/api/me/subscriptions", nil)
				req.Header.Set(InitDataHeader, "signed")
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)

				// 500 rather than 401: the caller did nothing wrong, and reporting it
				// as an auth failure would send an operator hunting the wrong bug.
				require.Equal(t, http.StatusInternalServerError, rr.Code)
				assert.False(t, ran, "a misconfigured middleware must still not serve")
			})
		}
	})
}

func TestUserIDFrom(t *testing.T) {
	t.Parallel()

	t.Run("reports absence rather than a zero id", func(t *testing.T) {
		t.Parallel()

		// Without the second result an unauthenticated request is indistinguishable
		// from user 0, which is exactly the confusion this contract exists to prevent.
		id, ok := UserIDFrom(t.Context())
		assert.False(t, ok)
		assert.Zero(t, id)
	})

	t.Run("round-trips the id the middleware stored", func(t *testing.T) {
		t.Parallel()

		id, ok := UserIDFrom(WithUserID(t.Context(), 77))
		require.True(t, ok)
		assert.Equal(t, int64(77), id)
	})

	t.Run("a zero id is still an authenticated caller", func(t *testing.T) {
		t.Parallel()

		id, ok := UserIDFrom(WithUserID(t.Context(), 0))
		assert.True(t, ok, "ok reports whether the request was authenticated, not whether the id is non-zero")
		assert.Zero(t, id)
	})
}
