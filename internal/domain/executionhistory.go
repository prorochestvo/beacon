package domain

import "time"

// ExecutionHistory records the outcome of one collector run for a rate source.
type ExecutionHistory struct {
	ID         string
	SourceName string
	Success    bool
	// Error holds the error message when Success is false; empty otherwise.
	Error     string
	Timestamp time.Time
}
