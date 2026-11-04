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

// SourceCollectionHealth summarises one source's recent collection outcomes: how long it
// has been since anything worked, and what has been going wrong since.
//
// It is derived from execution_history on every evaluation rather than stored, so it can
// never disagree with the rows it describes.
type SourceCollectionHealth struct {
	// SourceName identifies the source this describes.
	SourceName string
	// LastSuccessAt is when the source last collected successfully. Zero when it never
	// has — a source that has failed since the day it was added is unhealthy, not unknown.
	LastSuccessAt time.Time
	// LastRunAt is when the source was last attempted at all, successfully or not. Zero
	// when it has never run.
	LastRunAt time.Time
	// ConsecutiveFailures counts attempts since the last success. It is zero both for a
	// healthy source and for one that has silently stopped being attempted — which is why
	// staleness, not this counter, decides health. It is carried for the alert text, where
	// it separates "failing loudly" from "no longer running at all".
	ConsecutiveFailures int64
	// LastError is the message from the most recent failure; empty when there is none.
	LastError string
}

// HasRun reports whether the source has ever been attempted.
func (h SourceCollectionHealth) HasRun() bool { return !h.LastRunAt.IsZero() }
