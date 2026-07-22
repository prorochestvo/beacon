// Package internal provides shared cross-cutting utilities: structured errors
// with stack traces, user-facing PublicError, and the logger factory.
// It is imported by every application layer; keep it dependency-light.
package internal

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/prorochestvo/loginjector"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// ErrNotFound is returned when a requested entity does not exist in the data store.
var ErrNotFound = errors.New("not found")

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

// SetRuntimeDetailsProvider registers beacon's OS/runtime detail gatherer with
// loginjector, so every loginjector.StackTraceError carries the same CPU, memory,
// and process information the hand-rolled StackTraceError used to embed. loginjector
// invokes the provider at most once (on first StackTraceError construction) and caches
// the result for the process lifetime; registering again before that first use simply
// replaces the pending provider, and calls after it are no-ops. NewLogger calls this at
// startup for every binary, which is before any stack trace is built, so the details are
// always available. Safe to call more than once.
func SetRuntimeDetailsProvider() {
	loginjector.SetRuntimeDetailsProvider(runtimeDetails)
}

// runtimeDetails gathers OS, architecture, Go runtime, process, CPU, and memory
// information as a newline-joined string. It is the provider handed to loginjector.
func runtimeDetails() string {
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

	return strings.Join(details, "\n")
}
