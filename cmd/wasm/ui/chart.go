package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/seilbekskindirov/monitor/internal/dto"
)

// SparklineOptions controls the SVG viewBox dimensions and stroke/fill of the
// polyline rendered by RenderSparkline. Width and Height define the viewBox
// coordinate space only — actual layout size is controlled by the consumer's
// CSS (class="card-chart").
type SparklineOptions struct {
	Width       int
	Height      int
	StrokeColor string // CSS color string; empty defaults to "currentColor"
	FillColor   string // unused in v1; reserved for area fill
}

// RenderSparkline returns the inline SVG markup for a line chart of points.
// Edge cases: zero points → placeholder with "no data" text; one point or
// all-equal prices → horizontal line at midpoint. Never panics.
//
// Source names passed as the container id are admin-controlled slugs;
// the SVG itself contains no user-controlled strings — only numeric
// coordinates (formatted via strconv.FormatFloat) and the constant class name.
func RenderSparkline(points []dto.ChartPointResponse, opts SparklineOptions) string {
	if opts.Width <= 0 {
		opts.Width = 100
	}
	if opts.Height <= 0 {
		opts.Height = 30
	}
	stroke := opts.StrokeColor
	if stroke == "" {
		stroke = "currentColor"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="card-chart" viewBox="0 0 %d %d" preserveAspectRatio="none">`,
		opts.Width, opts.Height)

	if len(points) == 0 {
		fmt.Fprintf(&b, `<text x="%d" y="%d" text-anchor="middle" class="card-chart-empty">no data</text>`,
			opts.Width/2, opts.Height/2)
		b.WriteString(`</svg>`)
		return b.String()
	}

	minP, maxP := points[0].Price, points[0].Price
	for _, p := range points[1:] {
		if p.Price < minP {
			minP = p.Price
		}
		if p.Price > maxP {
			maxP = p.Price
		}
	}
	rangeP := maxP - minP

	coords := make([]string, 0, len(points))
	for i, p := range points {
		var x, y float64
		if len(points) == 1 {
			x = float64(opts.Width) / 2
		} else {
			x = float64(i) * float64(opts.Width) / float64(len(points)-1)
		}
		if rangeP == 0 {
			y = float64(opts.Height) / 2
		} else {
			// SVG y-axis is top-down: invert so higher prices render higher.
			y = float64(opts.Height) - (p.Price-minP)/rangeP*float64(opts.Height)
		}
		coords = append(coords,
			strconv.FormatFloat(x, 'f', 2, 64)+","+strconv.FormatFloat(y, 'f', 2, 64))
	}

	fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="1.5" points="%s"/>`,
		stroke, strings.Join(coords, " "))
	b.WriteString(`</svg>`)
	return b.String()
}
