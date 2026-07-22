package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var _ error = &PublicError{}

func TestNewPublicError(t *testing.T) {
	tests := []struct {
		name     string
		details  []string
		expected string
	}{
		{
			name:     "single detail",
			details:  []string{"error occurred"},
			expected: "error occurred",
		},
		{
			name:     "multiple details",
			details:  []string{"database", "connection", "failed"},
			expected: "database connection failed",
		},
		{
			name:     "empty details",
			details:  []string{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewPublicError(tt.details...)
			require.Equal(t, tt.expected, err.Details())
			require.Equal(t, tt.expected, err.Error())
		})
	}
}

func TestPublicError_Details(t *testing.T) {
	details := "user not found"
	err := NewPublicError(details)
	require.Equal(t, details, err.Details())
}

func TestPublicError_Error(t *testing.T) {
	details := "invalid request"
	err := NewPublicError(details)
	require.Equal(t, details, err.Error())
}

func TestRuntimeDetails(t *testing.T) {
	got := runtimeDetails()
	for _, part := range []string{"Go version:", "GOOS:", "GOARCH:", "PID:", "PPID:"} {
		require.Contains(t, got, part)
	}
}

// benchmarks
func BenchmarkNewPublicError(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewPublicError("test", "error", "message")
	}
}
