package managed

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

var (
	chartBgColor    = color.RGBA{R: 24, G: 24, B: 37, A: 255}   // #181825
	chartGridColor  = color.RGBA{R: 49, G: 50, B: 68, A: 255}   // #313244
	chartLineColor  = color.RGBA{R: 137, G: 180, B: 250, A: 255} // #89b4fa
	chartPointColor = color.RGBA{R: 180, G: 190, B: 254, A: 255} // #b4befe
	chartTextColor  = color.RGBA{R: 205, G: 214, B: 244, A: 255} // #cdd6f4
)

// RenderProgressChartPNG renders a dark-themed progress chart as a PNG.
func RenderProgressChartPNG(entries []store.AgentProgressEntry, width, height int) ([]byte, error) {
	if width <= 0 {
		width = 800
	}
	if height <= 0 {
		height = 400
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: chartBgColor}, image.Point{}, draw.Src)

	// If empty state, render centered "No progress recorded"
	if len(entries) == 0 {
		drawCenteredText(img, "No progress recorded", width/2, height/2, chartTextColor)
		return encodePNG(img)
	}

	// Chart padding & bounds
	// Margins: left for labels (e.g. "100%"), right, top, bottom
	paddingLeft := 50
	paddingRight := 30
	paddingTop := 30
	paddingBottom := 40

	plotLeft := paddingLeft
	plotRight := width - paddingRight
	plotTop := paddingTop
	plotBottom := height - paddingBottom

	if plotRight <= plotLeft {
		plotRight = plotLeft + 1
	}
	if plotBottom <= plotTop {
		plotBottom = plotTop + 1
	}

	plotWidth := plotRight - plotLeft
	plotHeight := plotBottom - plotTop

	// Draw Y-axis grid and percentage labels (0%, 25%, 50%, 75%, 100%)
	yTicks := []struct {
		percent int
		label   string
	}{
		{0, "0%"},
		{25, "25%"},
		{50, "50%"},
		{75, "75%"},
		{100, "100%"},
	}

	for _, tick := range yTicks {
		y := plotBottom - int(math.Round(float64(tick.percent)*float64(plotHeight)/100.0))
		// Draw grid line
		drawHLine(img, plotLeft, plotRight, y, chartGridColor)
		// Draw label right-aligned to plotLeft - 8
		drawLabelRightAligned(img, tick.label, plotLeft-8, y+4, chartTextColor)
	}

	// Calculate (x, y) coordinates for each entry
	type point struct {
		x, y int
	}
	pts := make([]point, len(entries))

	if len(entries) == 1 {
		// Single point plotted at 50% horizontal center
		x := plotLeft + plotWidth/2
		percent := clampPercent(entries[0].Percent)
		y := plotBottom - int(math.Round(float64(percent)*float64(plotHeight)/100.0))
		pts[0] = point{x: x, y: y}
	} else {
		// Parse timestamps
		times := make([]time.Time, len(entries))
		allParsed := true
		for i, e := range entries {
			t, err := parseTimestamp(e.Timestamp)
			if err != nil {
				allParsed = false
				break
			}
			times[i] = t
		}

		timeDeltas := false
		if allParsed {
			start := times[0]
			end := times[len(times)-1]
			totalDuration := end.Sub(start)
			if totalDuration > 0 {
				timeDeltas = true
				for i, e := range entries {
					ratio := float64(times[i].Sub(start)) / float64(totalDuration)
					if ratio < 0 {
						ratio = 0
					} else if ratio > 1 {
						ratio = 1
					}
					x := plotLeft + int(math.Round(ratio*float64(plotWidth)))
					percent := clampPercent(e.Percent)
					y := plotBottom - int(math.Round(float64(percent)*float64(plotHeight)/100.0))
					pts[i] = point{x: x, y: y}
				}
			}
		}

		if !timeDeltas {
			// Space evenly if timestamps are identical or failed to parse
			for i, e := range entries {
				ratio := float64(i) / float64(len(entries)-1)
				x := plotLeft + int(math.Round(ratio*float64(plotWidth)))
				percent := clampPercent(e.Percent)
				y := plotBottom - int(math.Round(float64(percent)*float64(plotHeight)/100.0))
				pts[i] = point{x: x, y: y}
			}
		}
	}

	// Draw lines connecting points
	for i := 0; i < len(pts)-1; i++ {
		drawThickLine(img, pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y, 2, chartLineColor)
	}

	// Draw points
	for _, p := range pts {
		drawCircle(img, p.x, p.y, 4, chartPointColor)
	}

	return encodePNG(img)
}

func clampPercent(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func parseTimestamp(ts string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, ts); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("failed to parse timestamp")
}

func drawHLine(img *image.RGBA, x0, x1, y int, c color.RGBA) {
	if y < 0 || y >= img.Bounds().Dy() {
		return
	}
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if x0 < 0 {
		x0 = 0
	}
	if x1 >= img.Bounds().Dx() {
		x1 = img.Bounds().Dx() - 1
	}
	for x := x0; x <= x1; x++ {
		img.SetRGBA(x, y, c)
	}
}

// Bresenham's line algorithm
func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx := 1
	if x0 > x1 {
		sx = -1
	}
	sy := 1
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy

	b := img.Bounds()

	for {
		if x0 >= 0 && x0 < b.Dx() && y0 >= 0 && y0 < b.Dy() {
			img.SetRGBA(x0, y0, c)
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func drawThickLine(img *image.RGBA, x0, y0, x1, y1, thickness int, c color.RGBA) {
	if thickness <= 1 {
		drawLine(img, x0, y0, x1, y1, c)
		return
	}
	offset := thickness / 2
	for ox := -offset; ox <= offset; ox++ {
		for oy := -offset; oy <= offset; oy++ {
			if ox*ox+oy*oy <= offset*offset+1 {
				drawLine(img, x0+ox, y0+oy, x1+ox, y1+oy, c)
			}
		}
	}
}

func drawCircle(img *image.RGBA, cx, cy, radius int, c color.RGBA) {
	b := img.Bounds()
	r2 := radius * radius
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx*dx+dy*dy <= r2 {
				x := cx + dx
				y := cy + dy
				if x >= 0 && x < b.Dx() && y >= 0 && y < b.Dy() {
					img.SetRGBA(x, y, c)
				}
			}
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func drawCenteredText(img *image.RGBA, text string, cx, cy int, c color.RGBA) {
	face := basicfont.Face7x13
	textWidth := font.MeasureString(face, text).Ceil()
	// basicfont Face7x13 has ascent 11, descent 2, total height ~13
	ascent := face.Metrics().Ascent.Ceil()
	descent := face.Metrics().Descent.Ceil()
	totalHeight := ascent + descent

	x := cx - textWidth/2
	y := cy - totalHeight/2 + ascent

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(text)
}

func drawLabelRightAligned(img *image.RGBA, text string, rightX, baselineY int, c color.RGBA) {
	face := basicfont.Face7x13
	textWidth := font.MeasureString(face, text).Ceil()
	x := rightX - textWidth

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, baselineY),
	}
	d.DrawString(text)
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
