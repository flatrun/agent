package observ

import (
	"fmt"
	"sort"
	"strings"
)

// prometheusContentType is the text exposition format Prometheus scrapes.
const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// metricHelp describes each series for anything reading the exposition.
var metricHelp = map[string]string{
	MetricCPUUsage:          "Container CPU usage as a percentage of host capacity.",
	MetricMemoryUsage:       "Container memory usage in bytes.",
	MetricMemoryLimit:       "Container memory limit in bytes.",
	MetricMemoryUtilization: "Container memory usage as a percentage of its effective limit.",
	MetricNetworkRx:         "Bytes received by the container per second.",
	MetricNetworkTx:         "Bytes sent by the container per second.",
}

// renderPrometheus writes the store's latest sample per series in Prometheus text exposition
// format, so any scraper can read the same numbers the native UI draws. Every series is a
// gauge, including network, which the store already reduces to a per-second rate.
func renderPrometheus(points []LatestPoint) string {
	byMetric := map[string][]LatestPoint{}
	for _, p := range points {
		byMetric[p.Metric] = append(byMetric[p.Metric], p)
	}

	names := make([]string, 0, len(byMetric))
	for name := range byMetric {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		exposed := prometheusMetricName(name)

		if help, ok := metricHelp[name]; ok {
			fmt.Fprintf(&b, "# HELP %s %s\n", exposed, escapeHelp(help))
		}
		fmt.Fprintf(&b, "# TYPE %s gauge\n", exposed)

		for _, p := range byMetric[name] {
			fmt.Fprintf(&b, "%s{deployment=\"%s\",container=\"%s\"} %g %d\n",
				exposed,
				escapeLabelValue(p.Deployment),
				escapeLabelValue(p.Container),
				p.Value,
				p.Time.UnixMilli(),
			)
		}
	}
	return b.String()
}

// prometheusMetricName converts an OpenTelemetry name to Prometheus's character set, which
// allows only letters, digits and underscores: "container.cpu.usage" becomes
// "container_cpu_usage".
func prometheusMetricName(name string) string {
	var b strings.Builder
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9' && i > 0:
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// escapeLabelValue escapes the three characters the exposition format reserves.
func escapeLabelValue(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// escapeHelp escapes the two characters a HELP line reserves.
func escapeHelp(v string) string {
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	return r.Replace(v)
}
