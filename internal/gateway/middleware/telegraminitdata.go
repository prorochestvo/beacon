package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/seilbekskindirov/beacon/internal/tools/tgwebapp"
)

// InitDataHeader is the only place a signed Telegram WebApp credential is accepted.
//
// It is deliberately not also read from the query string: initData carries an HMAC
// valid for hours, and a copy in the URL would be written to access logs and sent
// onward in Referer headers for that whole window.
const InitDataHeader = "X-Telegram-Init-Data"

// TelegramInitDataConfig carries what verifying a Telegram WebApp credential needs.
type TelegramInitDataConfig struct {
	// BotToken is the token the initData HMAC is verified against. Required.
	BotToken string

	// MaxAge is how old a signed payload may be. Required — a zero would accept a
	// credential of any age, so TelegramInitData rejects it rather than guessing.
	MaxAge time.Duration

	// Validate verifies the credential and returns the caller's Telegram id.
	// Defaults to tgwebapp.ValidateInitData; a fake here is how tests avoid holding
	// a real bot token. See Now for why this is a seam rather than a hard-wired call.
	Validate func(initData, botToken string, maxAge time.Duration, now time.Time) (int64, error)

	// Now supplies the clock the payload's age is measured against. Defaults to
	// time.Now. Together with Validate these are the two sanctioned test seams in
	// this package: the credential is signed with a real token and aged against the
	// wall clock, so a test that can substitute neither can only exercise expiry by
	// holding a production token and sleeping.
	Now func() time.Time
}

// TelegramInitData returns a middleware that authenticates the Telegram WebApp
// credential and puts the caller's id on the request context, where UserIDFrom
// reads it.
//
// It is mounted once over the whole /api/v1/me/ family rather than wrapped around each
// route. That is the point: the check used to be four lines copy-pasted into every
// handler, so a new handler that forgot them served another user's data with no
// error and no log line. Mounted, a route in that family is authenticated because of
// where it is registered, not because someone remembered.
//
// A failure answers 401 and the inner handler never runs, so a handler behind this
// can treat the id as established.
func TelegramInitData(cfg TelegramInitDataConfig) func(http.Handler) http.Handler {
	validate := cfg.Validate
	if validate == nil {
		validate = tgwebapp.ValidateInitData
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A non-positive MaxAge would hand every expiry check a window of zero or
			// forever depending on the validator; refuse instead of picking one. This
			// is a wiring fault, so it must not read as a caller's failed login.
			if cfg.MaxAge <= 0 {
				http.Error(w, `{"error":"auth is misconfigured"}`, http.StatusInternalServerError)
				return
			}

			userID, err := validate(r.Header.Get(InitDataHeader), cfg.BotToken, cfg.MaxAge, now())
			if err != nil {
				// The reason is deliberately not echoed: it would tell a prober which
				// part of a forged payload failed. Nor is the credential logged.
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r.WithContext(WithUserID(r.Context(), userID)))
		})
	}
}

// WithUserID returns ctx carrying userID as the authenticated caller.
//
// Exported for tests, which need a request that looks like it came through the
// middleware without signing a payload. Production code has no reason to call it:
// the middleware is the only writer.
func WithUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDContextKey{}, userID)
}

// UserIDFrom returns the authenticated caller's Telegram id, and whether the request
// carried one at all.
//
// The second result is not decoration: without it an unauthenticated request would
// be indistinguishable from user 0. Callers must treat !ok as "not authenticated"
// and refuse, never as a default.
func UserIDFrom(ctx context.Context) (int64, bool) {
	userID, ok := ctx.Value(userIDContextKey{}).(int64)
	return userID, ok
}

// userIDContextKey is the context key for the authenticated caller's id. An
// unexported struct type, so no other package can collide with it or plant a value
// under it — the context is an authentication result, not a shared bag.
type userIDContextKey struct{}
