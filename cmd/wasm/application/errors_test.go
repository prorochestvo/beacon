package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/monitor/cmd/wasm/apiclient"
	"github.com/seilbekskindirov/monitor/cmd/wasm/application"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

// fakeFetcherURLRecorder records every URL passed to FetchJSON for offset-shape
// assertions. FetchNoContent is unused here.
type fakeFetcherURLRecorder struct {
	jsonResponse []byte
	jsonErr      error
	urls         []string
	callCount    int
}

var _ apiclient.Fetcher = (*fakeFetcherURLRecorder)(nil)

func (f *fakeFetcherURLRecorder) FetchJSON(_ context.Context, _, url string, _ any, _ map[string]string) ([]byte, error) {
	f.callCount++
	f.urls = append(f.urls, url)
	if f.jsonErr != nil {
		return nil, f.jsonErr
	}
	return f.jsonResponse, nil
}

func (f *fakeFetcherURLRecorder) FetchNoContent(_ context.Context, _, _ string, _ any, _ map[string]string) error {
	return nil
}

func execErrorsFixture() []dto.ExecutionErrorResponse {
	return []dto.ExecutionErrorResponse{
		{ID: "1", SourceName: "usd-eur", Error: "timeout", Timestamp: "2026-01-01T00:00:00Z"},
		{ID: "2", SourceName: "gbp-usd", Error: "connect refused", Timestamp: "2026-01-02T00:00:00Z"},
	}
}

func eventErrorsFixture() []dto.NotificationResponse {
	return []dto.NotificationResponse{
		{ID: "a", UserType: "telegram", Status: "failed", LastError: "send failed", CreatedAt: time.Now(), SentAt: time.Now()},
	}
}

func TestErrorsPage_LoadExecPage(t *testing.T) {
	t.Parallel()

	execData, err := json.Marshal(execErrorsFixture())
	require.NoError(t, err)

	t.Run("happy path replaces ExecErrors and updates ExecPage", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: execData}
		p := application.NewErrorsPage(apiclient.New(f))
		err := p.LoadExecPage(t.Context(), 3)
		require.NoError(t, err)
		state := p.State()
		assert.Equal(t, 3, state.ExecPage)
		require.Len(t, state.ExecErrors, 2)
		assert.Equal(t, "1", state.ExecErrors[0].ID)
		assert.Equal(t, "2", state.ExecErrors[1].ID)
	})

	t.Run("error leaves state unchanged", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("network error")}
		p := application.NewErrorsPage(apiclient.New(f))
		err := p.LoadExecPage(t.Context(), 2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network error")
		state := p.State()
		assert.Equal(t, 1, state.ExecPage, "page must remain at initial value on error")
		assert.Empty(t, state.ExecErrors)
	})

	t.Run("issues exactly one FetchJSON call", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcherURLRecorder{jsonResponse: execData}
		p := application.NewErrorsPage(apiclient.New(f))
		require.NoError(t, p.LoadExecPage(t.Context(), 1))
		assert.Equal(t, 1, f.callCount)
	})
}

func TestErrorsPage_LoadEventPage(t *testing.T) {
	t.Parallel()

	eventData, err := json.Marshal(eventErrorsFixture())
	require.NoError(t, err)

	t.Run("happy path replaces EventErrors and updates EventPage", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonResponse: eventData}
		p := application.NewErrorsPage(apiclient.New(f))
		err := p.LoadEventPage(t.Context(), 2)
		require.NoError(t, err)
		state := p.State()
		assert.Equal(t, 2, state.EventPage)
		require.Len(t, state.EventErrors, 1)
		assert.Equal(t, "a", state.EventErrors[0].ID)
	})

	t.Run("error leaves state unchanged", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcher{jsonErr: errors.New("timeout")}
		p := application.NewErrorsPage(apiclient.New(f))
		err := p.LoadEventPage(t.Context(), 3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timeout")
		state := p.State()
		assert.Equal(t, 1, state.EventPage, "page must remain at initial value on error")
		assert.Empty(t, state.EventErrors)
	})

	t.Run("issues exactly one FetchJSON call", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcherURLRecorder{jsonResponse: eventData}
		p := application.NewErrorsPage(apiclient.New(f))
		require.NoError(t, p.LoadEventPage(t.Context(), 1))
		assert.Equal(t, 1, f.callCount)
	})

	t.Run("page 1 produces offset=0 and limit=50 in URL", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcherURLRecorder{jsonResponse: eventData}
		p := application.NewErrorsPage(apiclient.New(f))
		require.NoError(t, p.LoadEventPage(t.Context(), 1))
		require.Len(t, f.urls, 1)
		assert.Contains(t, f.urls[0], "offset=0", "page 1 must produce offset=0, got: %s", f.urls[0])
		assert.Contains(t, f.urls[0], "limit=50", "page 1 must produce limit=50, got: %s", f.urls[0])
	})

	t.Run("page 2 produces offset=50 and limit=50 in URL", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcherURLRecorder{jsonResponse: eventData}
		p := application.NewErrorsPage(apiclient.New(f))
		require.NoError(t, p.LoadEventPage(t.Context(), 2))
		require.Len(t, f.urls, 1)
		assert.Contains(t, f.urls[0], "offset=50", "page 2 must produce offset=50, got: %s", f.urls[0])
		assert.Contains(t, f.urls[0], "limit=50", "page 2 must produce limit=50, got: %s", f.urls[0])
	})

	t.Run("page 3 produces offset=100 and limit=50 in URL", func(t *testing.T) {
		t.Parallel()
		f := &fakeFetcherURLRecorder{jsonResponse: eventData}
		p := application.NewErrorsPage(apiclient.New(f))
		require.NoError(t, p.LoadEventPage(t.Context(), 3))
		require.Len(t, f.urls, 1)
		assert.Contains(t, f.urls[0], "offset=100", "page 3 must produce offset=100, got: %s", f.urls[0])
		assert.Contains(t, f.urls[0], "limit=50", "page 3 must produce limit=50, got: %s", f.urls[0])
	})
}
