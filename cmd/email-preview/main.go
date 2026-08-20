package main

import (
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"github.com/flatrun/agent/internal/notify"
)

func main() {
	output := flag.String("output", "email-previews", "directory for rendered HTML previews")
	flag.Parse()
	if err := os.MkdirAll(*output, 0755); err != nil {
		panic(err)
	}

	previews := map[string]notify.Notification{
		"generic.html": {
			Title: "Deployment update", Message: "The production deployment finished successfully.",
		},
		"positive.html": {
			Kind: notify.KindPositive, Title: "Service recovered",
			Message: "The API is healthy again and traffic has returned to normal.",
		},
		"negative.html": {
			Kind: notify.KindNegative, Title: "High CPU usage",
			Message: "The API has remained above its configured threshold for five minutes.",
		},
		"panels.html": {
			Kind: notify.KindNegative, Title: "Resource pressure detected",
			Message: "Review the affected service and its recent resource usage.",
			Panels: []notify.Panel{
				{Title: "CPU", Value: "92%", Detail: "Threshold: 80%"},
				{Title: "Last 15 minutes", Detail: "PNG graph preview", ImageURL: sampleGraph()},
			},
		},
	}

	for name, notification := range previews {
		body, err := notify.RenderEmail(notification)
		if err != nil {
			panic(err)
		}
		path := filepath.Join(*output, name)
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			panic(err)
		}
		fmt.Println(path)
	}
}

func sampleGraph() string {
	canvas := image.NewRGBA(image.Rect(0, 0, 520, 160))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{R: 248, G: 250, B: 252, A: 255}}, image.Point{}, draw.Src)
	values := []int{42, 55, 49, 68, 74, 70, 83, 92, 87, 94, 89, 92}
	line := color.RGBA{R: 185, G: 28, B: 28, A: 255}
	for i := 1; i < len(values); i++ {
		x0, y0 := 20+(i-1)*43, 140-values[i-1]
		x1, y1 := 20+i*43, 140-values[i]
		steps := x1 - x0
		for step := 0; step <= steps; step++ {
			x := x0 + step
			y := y0 + (y1-y0)*step/steps
			draw.Draw(canvas, image.Rect(x-1, y-1, x+2, y+2), &image.Uniform{C: line}, image.Point{}, draw.Src)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		panic(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
}
