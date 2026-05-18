// Package internal provides shared cross-cutting utilities: structured errors
// with stack traces, user-facing PublicError, HTTP-code errors, and the logger
// factory. It is imported by every application layer; keep it dependency-light.
package internal

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// NewTraceError creates a new TraceError with the current caller information.
// It captures the file name, line number, and function name where the error occurred.
func NewTraceError() *TraceError {
	pc, file, line, ok := runtime.Caller(1)
	var traceLine string
	if ok {
		fn := runtime.FuncForPC(pc)
		file = path.Base(file)
		traceLine = fmt.Sprintf("%s:%d (%s)", file, line, fn.Name())
	} else {
		traceLine = "unknown"
	}
	return &TraceError{line: traceLine}
}

// TraceError represents an error with a stack trace line.
// It stores the file, line number, and function name where the error was created.
type TraceError struct {
	line string
}

// Line returns the trace line string containing file, line number, and function name.
func (e *TraceError) Line() string { return e.line }

// Error implements the error interface.
func (e *TraceError) Error() string { return e.line }

// NewPublicError creates a new PublicError with the given details.
// Multiple detail strings are joined with spaces to form the error message.
func NewPublicError(details ...string) *PublicError {
	d := strings.Join(details, " ")
	return &PublicError{details: d}
}

// PublicError represents an error with user-facing details.
// It is intended for errors that can be safely shown to end users.
type PublicError struct {
	details string
}

// Details returns the user-facing error details.
func (e *PublicError) Details() string { return e.details }

// Error implements the error interface.
func (e *PublicError) Error() string { return e.details }

// NewHttpCodeError creates a new HttpCodeError with the given status code.
func NewHttpCodeError(code int) *HttpCodeError { return &HttpCodeError{code: code} }

// HttpCodeError represents an error with an associated HTTP status code.
// It is useful for mapping errors to HTTP responses.
type HttpCodeError struct {
	code int
}

// StatusCode returns the HTTP status code.
func (e *HttpCodeError) StatusCode() int { return e.code }

// Error returns the HTTP status text corresponding to the status code.
func (e *HttpCodeError) Error() string { return http.StatusText(e.code) }

// NewStackTraceError creates a new StackTraceError with the full stack trace.
// It captures the complete call stack and system information at the time of creation.
func NewStackTraceError() *StackTraceError {
	traceLines := strings.Split(strings.TrimSpace(string(debug.Stack())), "\n")
	return &StackTraceError{info: debugOSDetails, lines: traceLines}
}

// StackTraceError represents an error with a full stack trace and system information.
// It includes both the call stack and detailed system/runtime information.
type StackTraceError struct {
	info  string
	lines []string
}

// OSDetails returns the system and runtime information.
func (e *StackTraceError) OSDetails() string { return e.info }

// StackTrace returns the stack trace lines.
func (e *StackTraceError) StackTrace() []string { return e.lines }

// Error returns the full stack trace as a multi-line string.
func (e *StackTraceError) Error() string { return strings.Join(append(e.lines, e.info), "\n") }

// init initializes the debugOSDetails variable with system and runtime information.
// this information is collected once at package initialization and includes:
// go version, OS, architecture, CPU count, process IDs, CPU model, and memory stats.
func init() {
	details := []string{
		fmt.Sprintf("Go version: %s", runtime.Version()),
		fmt.Sprintf("GOOS: %s", runtime.GOOS),
		fmt.Sprintf("GOARCH: %s", runtime.GOARCH),
		fmt.Sprintf("NumCPU: %d", runtime.NumCPU()),
		fmt.Sprintf("GOMAXPROCS: %d", runtime.GOMAXPROCS(0)),
		fmt.Sprintf("Compiler: %s", runtime.Compiler),
		fmt.Sprintf("PID: %d", os.Getpid()),
		fmt.Sprintf("PPID: %d", os.Getppid()),
	}

	if cpuInfo, err := cpu.Info(); err == nil && len(cpuInfo) > 0 {
		for _, c := range cpuInfo {
			details = append(
				details,
				fmt.Sprintf("CPU: %s, %d cores", c.ModelName, c.Cores),
			)
		}
	}

	if vm, err := mem.VirtualMemory(); err == nil && vm != nil {
		details = append(
			details,
			fmt.Sprintf("Total RAM: %v MB", vm.Total/1024/1024),
			fmt.Sprintf("Used: %v MB", vm.Used/1024/1024),
		)

	}

	debugOSDetails = strings.Join(details, "\n")
}

// debugOSDetails contains cached system and runtime information collected at init time.
var debugOSDetails string

// ErrNotFound is returned when a requested entity does not exist in the data store.
var ErrNotFound = errors.New("not found")
