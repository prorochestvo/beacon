// Package application contains pure-Go page controllers for the WASM frontend.
// Each controller owns a typed State struct and exposes load/action methods that
// call the apiclient. The controllers have no syscall/js dependencies and are
// therefore testable under the host toolchain.
package application

import (
	"context"

	"github.com/seilbekskindirov/monitor/internal/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

const (
	// ExecLimit is the page size for the execution errors table.
	ExecLimit = 50
	// EventLimit is the page size for the event (notification) errors table.
	EventLimit = 50
)

// ErrorsState holds all client-side state for the Errors screen.
type ErrorsState struct {
	ExecErrors []api.ExecutionErrorResponse
	ExecPage   int

	EventErrors []api.NotificationResponse
	EventPage   int
}

// ErrorsPage is the page controller for the Errors screen. It owns ErrorsState
// and exposes load methods for each table. It has no DOM dependencies and is
// testable as plain Go.
type ErrorsPage struct {
	state  ErrorsState
	client *apiclient.Client
}

// NewErrorsPage constructs an empty ErrorsPage. Data arrives via LoadExecPage
// and LoadEventPage after construction.
func NewErrorsPage(client *apiclient.Client) *ErrorsPage {
	return &ErrorsPage{
		state: ErrorsState{
			ExecPage:  1,
			EventPage: 1,
		},
		client: client,
	}
}

// State returns a copy of the current state for reading by the UI layer.
func (p *ErrorsPage) State() ErrorsState { return p.state }

// LoadExecPage fetches the given page of execution errors and replaces the
// internal slice. On success ExecPage is updated to page.
func (p *ErrorsPage) LoadExecPage(ctx context.Context, page int) error {
	items, err := p.client.ListExecutionErrors(ctx, page)
	if err != nil {
		return err
	}
	p.state.ExecErrors = items
	p.state.ExecPage = page
	return nil
}

// LoadEventPage fetches the given page of failed notification events and
// replaces the internal slice. The server uses offset+limit pagination:
// offset = (page-1)*EventLimit, limit = EventLimit. On success EventPage is
// updated to page.
func (p *ErrorsPage) LoadEventPage(ctx context.Context, page int) error {
	offset := (page - 1) * EventLimit
	items, err := p.client.ListFailedNotifications(ctx, offset, EventLimit)
	if err != nil {
		return err
	}
	p.state.EventErrors = items
	p.state.EventPage = page
	return nil
}
