package internal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var _ error = &PublicError{}
var _ error = &TraceError{}
var _ error = &StackTraceError{}

func TestNewTraceError(t *testing.T) {
	err := NewTraceError()
	require.NotNil(t, err)
	require.NotEmpty(t, err.Error())
	require.NotEmpty(t, err.Line())
	require.Equal(t, err.Line(), err.Error())
	require.Contains(t, err.Line(), "errors_test.go")
	require.Contains(t, err.Line(), ":")
	require.Contains(t, err.Line(), "(")
	require.Contains(t, err.Line(), ")")
	require.Contains(t, err.Line(), "TestNewTraceError")
}

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

func TestNewStackTraceError(t *testing.T) {
	err := NewStackTraceError()
	require.NotNil(t, err)
	require.NotEmpty(t, err.StackTrace())
	require.NotEmpty(t, err.Error())
	require.Contains(t, err.Error(), "\n")
}

func TestStackTraceError_StackTrace(t *testing.T) {
	err := NewStackTraceError()
	trace := err.StackTrace()
	require.NotNil(t, trace)
	require.NotEmpty(t, trace)

	// check that first line typically contains "goroutine"
	if len(trace) > 0 {
		require.Contains(t, trace[0], "goroutine")
	}
}

func TestStackTraceError_Error(t *testing.T) {
	err := NewStackTraceError()
	errorMsg := err.Error()
	require.NotEmpty(t, errorMsg)

	trace := err.StackTrace()
	require.NotEmpty(t, trace)

	details := err.OSDetails()
	require.NotEmpty(t, details)

	require.Contains(t, errorMsg, strings.Join(trace, "\n"))
	require.Contains(t, errorMsg, details)
}

func TestDebugOSDetails(t *testing.T) {
	require.NotEmpty(t, debugOSDetails)

	// check for some expected content
	expectedParts := []string{
		"Go version:",
		"GOOS:",
		"GOARCH:",
		"NumCPU:",
		"PID:",
	}

	for _, part := range expectedParts {
		require.Contains(t, debugOSDetails, part)
	}
}

// benchmarks
func BenchmarkNewTraceError(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewTraceError()
	}
}

func BenchmarkNewPublicError(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewPublicError("test", "error", "message")
	}
}

func BenchmarkNewStackTraceError(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewStackTraceError()
	}
}
