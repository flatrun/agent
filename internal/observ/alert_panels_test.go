package observ

import (
	"testing"
	"time"
)

func TestAlertPanelsCarryReadingChartAndTopContainers(t *testing.T) {
	store := NewStore(1000)
	now := time.Now()
	key := SeriesKey{Deployment: "shop", Container: "shop-web", Metric: MetricCPUUsage}
	for i := 0; i < 300; i++ {
		store.add(key, Sample{Time: now.Add(-time.Hour + time.Duration(i)*12*time.Second), Value: float64(i % 100)})
	}
	ev := AlertEvent{
		Deployment: "shop", Container: "shop-web", Metric: MetricCPUUsage, Value: 92, Threshold: 80,
		Comparison: ComparisonAbove, At: now,
		Snapshot: []Consumer{{Container: "shop-web", Value: 60}, {Container: "shop-worker", Value: 15}},
	}
	panels := alertPanels(store, ev)
	if len(panels) != 3 {
		t.Fatalf("panels = %+v", panels)
	}
	if panels[0].Value != "92.0%" || panels[0].Detail != "Alerts when above 80.0%" {
		t.Errorf("reading panel = %+v", panels[0])
	}
	chart := panels[1].Chart
	if chart == nil || len(chart.Points) != alertChartMaxPoints || chart.Threshold == nil || *chart.Threshold != 80 {
		t.Errorf("chart panel = %+v", panels[1])
	}
	rows := panels[2].Rows
	if len(rows) != 2 || rows[0].Share != 1 || rows[1].Share != 0.25 || rows[1].Value != "15.0%" {
		t.Errorf("top containers = %+v", rows)
	}
	if got := alertPanels(nil, AlertEvent{Metric: MetricCPUUsage}); len(got) != 1 {
		t.Errorf("without history or snapshot = %+v", got)
	}
}
