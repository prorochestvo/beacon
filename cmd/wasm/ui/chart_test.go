package ui_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seilbekskindirov/monitor/cmd/wasm/ui"
	"github.com/seilbekskindirov/monitor/internal/dto"
)

func defaultOpts() ui.SparklineOptions {
	return ui.SparklineOptions{Width: 100, Height: 30}
}

func pts(prices ...float64) []dto.ChartPointResponse {
	out := make([]dto.ChartPointResponse, len(prices))
	for i, p := range prices {
		out[i] = dto.ChartPointResponse{Label: "2026-01-01", Price: p}
	}
	return out
}

func TestRenderSparkline(t *testing.T) {
	t.Parallel()

	t.Run("zero points renders empty placeholder", func(t *testing.T) {
		t.Parallel()
		svg := ui.RenderSparkline(nil, defaultOpts())
		assert.Contains(t, svg, "<svg")
		assert.Contains(t, svg, "no data")
		assert.NotContains(t, svg, "<polyline")
	})

	t.Run("one point renders horizontal line at midpoint", func(t *testing.T) {
		t.Parallel()
		svg := ui.RenderSparkline(pts(1.0), defaultOpts())
		assert.Contains(t, svg, "<polyline")
		// x = Width/2 = 50.00, y = Height/2 = 15.00 (range=0 path)
		assert.Contains(t, svg, "50.00,15.00")
	})

	t.Run("all equal prices renders horizontal line at midpoint", func(t *testing.T) {
		t.Parallel()
		svg := ui.RenderSparkline(pts(2.5, 2.5, 2.5), defaultOpts())
		assert.Contains(t, svg, "<polyline")
		// all y = Height/2 = 15.00
		assert.Contains(t, svg, "15.00")
		assert.NotContains(t, svg, "no data")
	})

	t.Run("two points renders ascending diagonal", func(t *testing.T) {
		t.Parallel()
		// prices 0→1: min=0 max=1 range=1
		// point 0: x=0.00, y = 30 - (0-0)/1*30 = 30.00
		// point 1: x=100.00, y = 30 - (1-0)/1*30 = 0.00
		svg := ui.RenderSparkline(pts(0.0, 1.0), defaultOpts())
		assert.Contains(t, svg, "0.00,30.00")
		assert.Contains(t, svg, "100.00,0.00")
	})

	t.Run("price range scales to viewBox height", func(t *testing.T) {
		t.Parallel()
		// min=10 max=20 range=10, height=30
		// point 0 (price=10): y = 30 - (10-10)/10*30 = 30.00
		// point 2 (price=20): y = 30 - (20-10)/10*30 = 0.00
		opts := ui.SparklineOptions{Width: 100, Height: 30}
		svg := ui.RenderSparkline(pts(10.0, 15.0, 20.0), opts)
		assert.Contains(t, svg, "0.00,30.00")
		assert.Contains(t, svg, "100.00,0.00")
		// midpoint price=15: y = 30 - (15-10)/10*30 = 30 - 15 = 15.00
		assert.Contains(t, svg, "15.00")
	})

	t.Run("negative prices are handled", func(t *testing.T) {
		t.Parallel()
		// min=-2 max=-1 range=1
		// point 0 (price=-2): y = 30 - (-2 - (-2))/1*30 = 30.00
		// point 1 (price=-1): y = 30 - (-1 - (-2))/1*30 = 0.00
		svg := ui.RenderSparkline(pts(-2.0, -1.0), defaultOpts())
		assert.Contains(t, svg, "<polyline")
		assert.Contains(t, svg, "0.00,30.00")
		assert.Contains(t, svg, "100.00,0.00")
	})

	t.Run("viewBox attribute matches Width and Height options", func(t *testing.T) {
		t.Parallel()
		opts := ui.SparklineOptions{Width: 200, Height: 60}
		svg := ui.RenderSparkline(pts(1.0), opts)
		assert.Contains(t, svg, `viewBox="0 0 200 60"`)
	})

	t.Run("output contains class=\"card-chart\"", func(t *testing.T) {
		t.Parallel()
		svg := ui.RenderSparkline(pts(1.0), defaultOpts())
		assert.Contains(t, svg, `class="card-chart"`)
	})

	t.Run("preserveAspectRatio is none", func(t *testing.T) {
		t.Parallel()
		svg := ui.RenderSparkline(pts(1.0, 2.0), defaultOpts())
		assert.Contains(t, svg, `preserveAspectRatio="none"`)
	})

	t.Run("custom stroke color appears in polyline", func(t *testing.T) {
		t.Parallel()
		opts := ui.SparklineOptions{Width: 100, Height: 30, StrokeColor: "#ff0000"}
		svg := ui.RenderSparkline(pts(1.0, 2.0), opts)
		assert.Contains(t, svg, `stroke="#ff0000"`)
	})

	t.Run("empty stroke color defaults to currentColor", func(t *testing.T) {
		t.Parallel()
		svg := ui.RenderSparkline(pts(1.0, 2.0), defaultOpts())
		assert.Contains(t, svg, `stroke="currentColor"`)
	})

	t.Run("default Width and Height applied when zero options passed", func(t *testing.T) {
		t.Parallel()
		svg := ui.RenderSparkline(pts(1.0), ui.SparklineOptions{})
		// defaults: 100x30
		assert.Contains(t, svg, `viewBox="0 0 100 30"`)
		// one point, x=Width/2=50.00
		assert.True(t, strings.Contains(svg, "50.00"), "expected midpoint x=50.00 in: "+svg)
	})
}
