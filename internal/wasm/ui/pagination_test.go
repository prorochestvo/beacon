package ui_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seilbekskindirov/monitor/internal/wasm/ui"
)

func TestRenderPagination(t *testing.T) {
	t.Parallel()

	t.Run("no buttons when page 1 and count less than limit", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 1, Count: 10, Limit: 25, Section: "subs"})
		assert.Equal(t, "", got)
	})

	t.Run("no buttons when page 1 and count equals zero", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 1, Count: 0, Limit: 25, Section: "subs"})
		assert.Equal(t, "", got)
	})

	t.Run("next only on first page when count equals limit", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 1, Count: 25, Limit: 25, Section: "subs"})
		assert.Contains(t, got, `data-section="subs"`)
		assert.Contains(t, got, `data-page="2"`)
		assert.Contains(t, got, "Next ›")
		assert.Contains(t, got, "‹ Prev")
		assert.Contains(t, got, "disabled")
		assert.NotContains(t, got, `data-page="0"`)
	})

	t.Run("next only on first page when count greater than limit", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 1, Count: 50, Limit: 25, Section: "events"})
		assert.Contains(t, got, `data-section="events"`)
		assert.Contains(t, got, `data-page="2"`)
		assert.Contains(t, got, "Next ›")
		assert.Contains(t, got, "disabled")
	})

	t.Run("prev only on last page when count less than limit", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 3, Count: 5, Limit: 25, Section: "subs"})
		assert.Contains(t, got, `data-section="subs"`)
		assert.Contains(t, got, `data-page="2"`)
		assert.Contains(t, got, "‹ Prev")
		assert.NotContains(t, got, "Next ›")
		assert.NotContains(t, got, "disabled")
	})

	t.Run("both prev and next on middle page", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 2, Count: 25, Limit: 25, Section: "subs"})
		assert.Contains(t, got, `data-page="1"`)
		assert.Contains(t, got, `data-page="3"`)
		assert.Contains(t, got, "‹ Prev")
		assert.Contains(t, got, "Next ›")
		assert.NotContains(t, got, "disabled")
	})

	t.Run("limit 50 parametricity - next only page 1", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 1, Count: 50, Limit: 50, Section: "errors"})
		assert.Contains(t, got, `data-section="errors"`)
		assert.Contains(t, got, `data-page="2"`)
		assert.Contains(t, got, "disabled")
	})

	t.Run("limit 50 parametricity - no buttons when count less than limit", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 1, Count: 49, Limit: 50, Section: "errors"})
		assert.Equal(t, "", got)
	})

	t.Run("limit 50 parametricity - prev only on page 4", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 4, Count: 10, Limit: 50, Section: "errors"})
		assert.Contains(t, got, `data-page="3"`)
		assert.Contains(t, got, "‹ Prev")
		assert.NotContains(t, got, "Next ›")
	})

	t.Run("section attribute is embedded in data-section for delegated click routing", func(t *testing.T) {
		t.Parallel()
		got := ui.RenderPagination(ui.PaginationState{Page: 2, Count: 25, Limit: 25, Section: "daily-events"})
		assert.Contains(t, got, `data-section="daily-events"`)
	})
}
