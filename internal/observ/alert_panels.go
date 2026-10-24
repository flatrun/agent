package observ

import (
	"fmt"
	"time"
)

// alertPanel matches the panel shape the agent's notifications accept, kept
// here so the plugin does not link the agent's mail stack.
type alertPanel struct {
	Title  string         `json:"title,omitempty"`
	Value  string         `json:"value,omitempty"`
	Detail string         `json:"detail,omitempty"`
	Chart  *alertChart    `json:"chart,omitempty"`
	Rows   []alertRowItem `json:"rows,omitempty"`
}

type alertChart struct {
	Points    []float64 `json:"points"`
	Threshold *float64  `json:"threshold,omitempty"`
}

type alertRowItem struct {
	Label string  `json:"label"`
	Value string  `json:"value"`
	Share float64 `json:"share,omitempty"`
}

const (
	alertChartWindow    = time.Hour
	alertChartMaxPoints = 120
)

func alertPanels(store *Store, ev AlertEvent) []alertPanel {
	threshold := ev.Threshold
	panels := []alertPanel{{
		Title:  "Now",
		Value:  formatMetricValue(ev.Metric, ev.Value),
		Detail: fmt.Sprintf("Alerts when %s %s", ev.Comparison, formatMetricValue(ev.Metric, ev.Threshold)),
	}}
	if store != nil {
		samples := store.Range(SeriesKey{Deployment: ev.Deployment, Container: ev.Container, Metric: ev.Metric}, ev.At.Add(-alertChartWindow))
		if points := downsample(samples, alertChartMaxPoints); len(points) >= 2 {
			panels = append(panels, alertPanel{
				Title:  "Last hour",
				Detail: "The dashed line is the alert threshold.",
				Chart:  &alertChart{Points: points, Threshold: &threshold},
			})
		}
	}
	if len(ev.Snapshot) > 0 {
		metric := consumerMetric(ev.Metric)
		top := ev.Snapshot[0].Value
		rows := make([]alertRowItem, 0, len(ev.Snapshot))
		for _, c := range ev.Snapshot {
			share := 0.0
			if top > 0 {
				share = c.Value / top
			}
			rows = append(rows, alertRowItem{Label: c.Container, Value: formatMetricValue(metric, c.Value), Share: share})
		}
		panels = append(panels, alertPanel{Title: "Top containers", Rows: rows})
	}
	return panels
}

// downsample keeps at most n points by averaging equal runs of samples.
func downsample(samples []Sample, n int) []float64 {
	if len(samples) <= n {
		points := make([]float64, len(samples))
		for i, s := range samples {
			points[i] = s.Value
		}
		return points
	}
	points := make([]float64, n)
	for i := range points {
		start, end := i*len(samples)/n, (i+1)*len(samples)/n
		var sum float64
		for _, s := range samples[start:end] {
			sum += s.Value
		}
		points[i] = sum / float64(end-start)
	}
	return points
}
