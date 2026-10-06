package web

import (
	"fmt"
	"html/template"
	"strings"
)

const (
	sparkWidth  = 640
	sparkHeight = 100
)

// sparkline renders values as a minimal inline SVG line chart. No JS
// charting library: this is the whole "history" view for now, and it is
// enough to see trends and outages at a glance.
func sparkline(values []float64, unit string) template.HTML {
	if len(values) < 2 {
		return template.HTML(`<p style="color:var(--muted)">not enough data yet</p>`)
	}

	min, max := values[0], values[0]
	for _, v := range values {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	// scaleMax only widens the range used for the y-coordinate math, so a
	// perfectly flat series draws a flat line instead of dividing by zero -
	// the displayed min/max labels below stay the real, un-widened values.
	scaleMax := max
	if scaleMax == min {
		scaleMax = min + 1
	}

	var pts strings.Builder
	n := len(values)
	for i, v := range values {
		x := float64(i) / float64(n-1) * sparkWidth
		y := sparkHeight - (v-min)/(scaleMax-min)*sparkHeight
		if i > 0 {
			pts.WriteByte(' ')
		}
		fmt.Fprintf(&pts, "%.1f,%.1f", x, y)
	}

	svg := fmt.Sprintf(
		`<svg viewBox="0 0 %d %d" width="100%%" height="%d" preserveAspectRatio="none" style="display:block">`+
			`<polyline points="%s" fill="none" stroke="#58a6ff" stroke-width="2" vector-effect="non-scaling-stroke"/>`+
			`</svg>`+
			`<div style="display:flex;justify-content:space-between;color:var(--muted);font-size:0.8rem">`+
			`<span>%.1f%s</span><span>%.1f%s</span></div>`,
		sparkWidth, sparkHeight, sparkHeight, pts.String(), min, unit, max, unit)
	return template.HTML(svg)
}
