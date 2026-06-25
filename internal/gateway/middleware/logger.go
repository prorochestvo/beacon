// Package middleware bundles HTTP middlewares wrapped around the v1 mux:
// access logging plus any future cross-cutting handlers.
package middleware

import (
	"io"
	"log"
	"net/http"
)

// Logger returns an HTTP middleware that emits one line per request to logger
// in the form:
//
//	middleware [STATUS] METHOD PATH
//
// The standard logger prefix supplies the timestamp, so a typical line reads
// "YYYY/MM/DD HH:MM:SS middleware [200] GET /api/sources". The status defaults
// to 200 when the inner handler writes the body without an explicit WriteHeader
// call, mirroring the net/http default.
//
// logger is typically the binary's shared log io.Writer; passing a
// *bytes.Buffer keeps tests hermetic.
func Logger(next http.Handler, logger io.Writer) http.Handler {
	l := log.New(logger, "middleware ", log.Lmsgprefix)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlPath := r.URL.Path
		rw := &httpResponseWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		l.Printf("[%.3d] %s %s\n", rw.statusCode, r.Method, urlPath)
	})
}

// httpResponseWriter wraps http.ResponseWriter so Logger can read the status
// code the inner handler actually sent. WriteHeader is a no-op after the first
// call, matching net/http's behaviour.
type httpResponseWriter struct {
	statusCode  int
	wroteHeader bool
	http.ResponseWriter
}

func (l *httpResponseWriter) WriteHeader(statusCode int) {
	if l.wroteHeader {
		return
	}
	l.wroteHeader = true
	l.statusCode = statusCode
	l.ResponseWriter.WriteHeader(statusCode)
}

func (l *httpResponseWriter) Write(b []byte) (int, error) {
	if !l.wroteHeader {
		l.WriteHeader(http.StatusOK)
	}
	return l.ResponseWriter.Write(b)
}
