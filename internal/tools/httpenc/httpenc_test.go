package httpenc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seilbekskindirov/beacon/internal/tools/httpenc"
)

func TestAcceptsGzip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{"empty header declines", "", false},
		{"plain gzip accepts", "gzip", true},
		{"gzip with deflate accepts", "gzip, deflate", true},
		{"gzip with q=1.0 accepts", "gzip;q=1.0", true},
		{"gzip with q=0.5 accepts", "gzip;q=0.5", true},
		{"gzip with q=0 declines", "gzip;q=0", false},
		{"gzip with q=0.0 declines", "gzip;q=0.0", false},
		{"gzip with q=0.00 declines", "gzip;q=0.00", false},
		{"GZIP uppercase accepts", "GZIP", true},
		{"only deflate declines gzip", "deflate", false},
		{"identity only declines gzip", "identity", false},
		{"deflate then gzip with q=0 declines", "deflate, gzip;q=0", false},
		{"first matching token wins", "gzip;q=0, gzip", false},
		{"surrounding whitespace tolerated", "  gzip ;  q=0.7 ", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, httpenc.AcceptsGzip(tc.header))
		})
	}
}
