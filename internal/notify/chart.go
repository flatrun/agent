package notify

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"golang.org/x/image/vector"
)

// Chart is a line of recent readings drawn into the email as an image, since
// email clients run no scripts and most strip SVG.
type Chart struct {
	Points    []float64 `json:"points"`
	Threshold *float64  `json:"threshold,omitempty"`
}

const (
	chartWidth  = 1072
	chartHeight = 300
	chartPad    = 12
)

func chartPNG(chart Chart, accent color.RGBA) (string, bool) {
	if len(chart.Points) < 2 {
		return "", false
	}
	low, high := chart.Points[0], chart.Points[0]
	for _, v := range chart.Points {
		low, high = math.Min(low, v), math.Max(high, v)
	}
	if chart.Threshold != nil {
		low, high = math.Min(low, *chart.Threshold), math.Max(high, *chart.Threshold)
	}
	if low > 0 {
		low = 0
	}
	if high == low {
		high = low + 1
	}
	high += (high - low) * 0.08
	x := func(i int) float32 {
		return chartPad + float32(i)*float32(chartWidth-2*chartPad)/float32(len(chart.Points)-1)
	}
	y := func(v float64) float32 {
		return float32(chartHeight-chartPad) - float32((v-low)/(high-low))*float32(chartHeight-2*chartPad)
	}

	canvas := image.NewRGBA(image.Rect(0, 0, chartWidth, chartHeight))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{248, 250, 252, 255}), image.Point{}, draw.Src)
	for i := 1; i < 4; i++ {
		row := chartPad + i*(chartHeight-2*chartPad)/4
		draw.Draw(canvas, image.Rect(chartPad, row, chartWidth-chartPad, row+1), image.NewUniform(color.RGBA{226, 232, 240, 255}), image.Point{}, draw.Src)
	}

	fill := vector.NewRasterizer(chartWidth, chartHeight)
	fill.MoveTo(x(0), float32(chartHeight-chartPad))
	for i, v := range chart.Points {
		fill.LineTo(x(i), y(v))
	}
	fill.LineTo(x(len(chart.Points)-1), float32(chartHeight-chartPad))
	fill.ClosePath()
	fill.Draw(canvas, canvas.Bounds(), image.NewUniform(color.NRGBA{accent.R, accent.G, accent.B, 40}), image.Point{})

	if chart.Threshold != nil {
		ty := y(*chart.Threshold)
		for start := float32(chartPad); start < chartWidth-chartPad; start += 22 {
			stroke(canvas, [][2]float32{{start, ty}, {start + 12, ty}}, 3, color.RGBA{100, 116, 139, 255})
		}
	}
	line := make([][2]float32, len(chart.Points))
	for i, v := range chart.Points {
		line[i] = [2]float32{x(i), y(v)}
	}
	stroke(canvas, line, 5, accent)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		return "", false
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()), true
}

// stroke draws a polyline of the given width. Each piece is drawn on its own,
// because overlapping outlines in one rasterizer pass can cancel out.
func stroke(dst draw.Image, points [][2]float32, width float32, c color.RGBA) {
	half := width / 2
	src := image.NewUniform(c)
	w, h := dst.Bounds().Dx(), dst.Bounds().Dy()
	r := vector.NewRasterizer(w, h)
	drawPiece := func(path func()) {
		r.Reset(w, h)
		path()
		r.Draw(dst, dst.Bounds(), src, image.Point{})
	}
	for i, p := range points {
		if i > 0 {
			a := points[i-1]
			dx, dy := p[0]-a[0], p[1]-a[1]
			if length := float32(math.Hypot(float64(dx), float64(dy))); length > 0 {
				nx, ny := -dy/length*half, dx/length*half
				drawPiece(func() {
					r.MoveTo(a[0]+nx, a[1]+ny)
					r.LineTo(p[0]+nx, p[1]+ny)
					r.LineTo(p[0]-nx, p[1]-ny)
					r.LineTo(a[0]-nx, a[1]-ny)
					r.ClosePath()
				})
			}
		}
		drawPiece(func() { circle(r, p[0], p[1], half) })
	}
}

func circle(r *vector.Rasterizer, cx, cy, radius float32) {
	const steps = 12
	for i := 0; i <= steps; i++ {
		angle := 2 * math.Pi * float64(i) / steps
		px, py := cx+radius*float32(math.Cos(angle)), cy+radius*float32(math.Sin(angle))
		if i == 0 {
			r.MoveTo(px, py)
		} else {
			r.LineTo(px, py)
		}
	}
	r.ClosePath()
}
