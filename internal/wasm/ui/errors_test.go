package ui_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/seilbekskindirov/monitor/internal/wasm/application"
	"github.com/seilbekskindirov/monitor/internal/wasm/ui"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

func makeErrorsState() application.ErrorsState {
	return application.ErrorsState{
		ExecPage:  1,
		EventPage: 1,
	}
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()

	t.Run("renders breadcrumb back link", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		html := ui.RenderErrors(state)
		assert.Contains(t, html, `href="#/"`)
		assert.Contains(t, html, "← All Sources")
	})

	t.Run("contains h1 Errors", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		html := ui.RenderErrors(state)
		assert.Contains(t, html, "<h1>Errors</h1>")
	})

	t.Run("contains stable div ids for async section updates", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		html := ui.RenderErrors(state)
		assert.Contains(t, html, `id="exec-errors-section"`)
		assert.Contains(t, html, `id="event-errors-section"`)
	})
}

func TestRenderExecErrorsSection(t *testing.T) {
	t.Parallel()

	t.Run("empty errors shows no-errors message", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		html := ui.RenderExecErrorsSection(state)
		assert.Contains(t, html, "No execution errors.")
	})

	t.Run("renders one row with correct columns", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.ExecErrors = []api.ExecutionErrorResponse{
			{ID: "42", SourceName: "usd-eur", Error: "timeout", Timestamp: "2026-01-15T12:00:00Z"},
		}
		html := ui.RenderExecErrorsSection(state)
		assert.Contains(t, html, "42")
		assert.Contains(t, html, "usd-eur")
		assert.Contains(t, html, "timeout")
		assert.Contains(t, html, `class="err"`)
		assert.Contains(t, html, "2026")
	})

	t.Run("XSS payload in error field is escaped", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.ExecErrors = []api.ExecutionErrorResponse{
			{ID: "1", SourceName: "src", Error: "<script>alert(1)</script>", Timestamp: "2026-01-01T00:00:00Z"},
		}
		html := ui.RenderExecErrorsSection(state)
		assert.NotContains(t, html, "<script>alert(1)</script>")
		assert.Contains(t, html, "&lt;script&gt;alert(1)&lt;/script&gt;")
	})

	t.Run("XSS payload with ampersand and quotes in error is escaped", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.ExecErrors = []api.ExecutionErrorResponse{
			{ID: "2", SourceName: "src", Error: `foo & bar "<>`, Timestamp: "2026-01-01T00:00:00Z"},
		}
		html := ui.RenderExecErrorsSection(state)
		assert.NotContains(t, html, `foo & bar "<>`)
		assert.Contains(t, html, "foo &amp; bar")
		assert.Contains(t, html, "&quot;")
		assert.Contains(t, html, "&lt;&gt;")
	})

	t.Run("XSS payload in source_name is escaped", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.ExecErrors = []api.ExecutionErrorResponse{
			{ID: "3", SourceName: "<b>src</b>", Error: "err", Timestamp: "2026-01-01T00:00:00Z"},
		}
		html := ui.RenderExecErrorsSection(state)
		assert.NotContains(t, html, "<b>src</b>")
		assert.Contains(t, html, "&lt;b&gt;src&lt;/b&gt;")
	})

	t.Run("no inline onclick attributes", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.ExecErrors = []api.ExecutionErrorResponse{
			{ID: "1", SourceName: "src", Error: "err", Timestamp: "2026-01-01T00:00:00Z"},
		}
		html := ui.RenderExecErrorsSection(state)
		assert.NotContains(t, html, "onclick")
	})

	t.Run("pagination renders data-section exec and data-page 2 when on page 1 with full result set", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.ExecPage = 1
		state.ExecErrors = make([]api.ExecutionErrorResponse, application.ExecLimit)
		html := ui.RenderExecErrorsSection(state)
		assert.Contains(t, html, `data-section="exec"`)
		assert.Contains(t, html, `data-page="2"`)
		assert.NotContains(t, html, "onclick")
	})
}

func TestRenderEventErrorsSection(t *testing.T) {
	t.Parallel()

	t.Run("empty errors shows no-errors message", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		html := ui.RenderEventErrorsSection(state)
		assert.Contains(t, html, "No event errors.")
	})

	t.Run("renders one row with correct columns", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.EventErrors = []api.NotificationResponse{
			{ID: "99", UserType: "telegram", LastError: "send failed", SentAt: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)},
		}
		html := ui.RenderEventErrorsSection(state)
		assert.Contains(t, html, "99")
		assert.Contains(t, html, "telegram")
		assert.Contains(t, html, "send failed")
		assert.Contains(t, html, `class="err"`)
		assert.Contains(t, html, "2026")
	})

	t.Run("XSS payload in last_error field is escaped", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.EventErrors = []api.NotificationResponse{
			{ID: "1", UserType: "tg", LastError: "<script>alert(1)</script>"},
		}
		html := ui.RenderEventErrorsSection(state)
		assert.NotContains(t, html, "<script>alert(1)</script>")
		assert.Contains(t, html, "&lt;script&gt;alert(1)&lt;/script&gt;")
	})

	t.Run("XSS payload with ampersand and quotes in last_error is escaped", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.EventErrors = []api.NotificationResponse{
			{ID: "2", UserType: "tg", LastError: `foo & bar "<>`},
		}
		html := ui.RenderEventErrorsSection(state)
		assert.NotContains(t, html, `foo & bar "<>`)
		assert.Contains(t, html, "foo &amp; bar")
		assert.Contains(t, html, "&quot;")
		assert.Contains(t, html, "&lt;&gt;")
	})

	t.Run("XSS payload in user_type is escaped", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.EventErrors = []api.NotificationResponse{
			{ID: "3", UserType: "<script>", LastError: "err"},
		}
		html := ui.RenderEventErrorsSection(state)
		assert.NotContains(t, html, "<script>")
		assert.Contains(t, html, "&lt;script&gt;")
	})

	t.Run("zero SentAt renders em dash", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.EventErrors = []api.NotificationResponse{
			{ID: "1", UserType: "tg", LastError: "err"},
		}
		html := ui.RenderEventErrorsSection(state)
		assert.Contains(t, html, "—")
	})

	t.Run("no inline onclick attributes", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.EventErrors = []api.NotificationResponse{
			{ID: "1", UserType: "tg", LastError: "err"},
		}
		html := ui.RenderEventErrorsSection(state)
		assert.NotContains(t, html, "onclick")
	})

	t.Run("pagination renders data-section event and data-page 2 when on page 1 with full result set", func(t *testing.T) {
		t.Parallel()
		state := makeErrorsState()
		state.EventPage = 1
		state.EventErrors = make([]api.NotificationResponse, application.EventLimit)
		html := ui.RenderEventErrorsSection(state)
		assert.Contains(t, html, `data-section="event"`)
		assert.Contains(t, html, `data-page="2"`)
		assert.NotContains(t, html, "onclick")
	})
}
