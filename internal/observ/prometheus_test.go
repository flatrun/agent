package observ

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPrometheus(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	out := renderPrometheus([]LatestPoint{
		{SeriesKey: SeriesKey{Deployment: "shop", Container: "shop-web", Metric: MetricCPUUsage},
			Sample: Sample{Time: at, Value: 12.5}},
		{SeriesKey: SeriesKey{Deployment: "shop", Container: "shop-db", Metric: MetricMemoryUsage},
			Sample: Sample{Time: at, Value: 5_368_709_120}},
	})

	// Prometheus allows only letters, digits and underscores, so the OTel dots convert.
	if !strings.Contains(out, "# TYPE container_cpu_usage gauge") {
		t.Errorf("missing TYPE line for the converted name:\n%s", out)
	}
	if !strings.Contains(out, "# HELP container_cpu_usage Container CPU usage") {
		t.Errorf("missing HELP line:\n%s", out)
	}
	if strings.Contains(out, "container.cpu.usage") {
		t.Errorf("emitted a name Prometheus cannot parse:\n%s", out)
	}

	// Timestamps are milliseconds since the epoch.
	want := `container_cpu_usage{deployment="shop",container="shop-web"} 12.5 1700000000000`
	if !strings.Contains(out, want) {
		t.Errorf("missing sample line %q in:\n%s", want, out)
	}

	// A large byte count must not arrive in exponent form the scraper would misread.
	if !strings.Contains(out, `container_memory_usage{deployment="shop",container="shop-db"} 5.36870912e+09`) {
		t.Errorf("memory sample line wrong:\n%s", out)
	}
}

func TestRenderPrometheusEscapesLabelValues(t *testing.T) {
	// A deployment name is a directory name, but it reaches the scraper as a label value,
	// so anything that would break the line has to be escaped rather than emitted.
	out := renderPrometheus([]LatestPoint{
		{SeriesKey: SeriesKey{Deployment: `a"b\c`, Container: "x", Metric: MetricCPUUsage},
			Sample: Sample{Time: time.Unix(1, 0), Value: 1}},
	})

	if !strings.Contains(out, `deployment="a\"b\\c"`) {
		t.Errorf("label value not escaped:\n%s", out)
	}
	// One sample line, not a line broken in half by the quote.
	if got := strings.Count(out, "container_cpu_usage{"); got != 1 {
		t.Errorf("expected 1 sample line, got %d:\n%s", got, out)
	}
}

func TestPrometheusMetricName(t *testing.T) {
	tests := map[string]string{
		MetricCPUUsage:    "container_cpu_usage",
		MetricMemoryUsage: "container_memory_usage",
		MetricNetworkRx:   "container_network_io_rx",
		"weird-name.2":    "weird_name_2",
		// A digit cannot start a Prometheus metric name.
		"2fast": "_fast",
	}
	for in, want := range tests {
		if got := prometheusMetricName(in); got != want {
			t.Errorf("prometheusMetricName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderPrometheusEmpty(t *testing.T) {
	if out := renderPrometheus(nil); out != "" {
		t.Errorf("expected empty output for no series, got %q", out)
	}
}
