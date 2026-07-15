package observ

import (
	"sort"
	"time"
)

// MetricSeries is one metric's aligned time series across containers: a shared timestamp
// axis and, per container, a value at each timestamp (nil for gaps). This is the shape a
// time-series chart consumes directly.
type MetricSeries struct {
	Containers []string     `json:"containers"`
	Timestamps []int64      `json:"timestamps"` // unix seconds, ascending
	Values     [][]*float64 `json:"values"`     // [container][timestamp]
}

// TimeSeriesResponse is the batch of a deployment's metric series over a window.
type TimeSeriesResponse struct {
	Deployment string                  `json:"deployment"`
	Metrics    map[string]MetricSeries `json:"metrics"`
}

// buildTimeSeries gathers every series for a deployment (all if empty) since a time and
// aligns each metric's containers onto a shared timestamp axis.
// sampleSource is where a chart's data comes from: the in-memory window, or the stored
// history when the range reaches past it. Store satisfies this as it stands.
type sampleSource interface {
	Series() []SeriesKey
	Range(key SeriesKey, since time.Time) []Sample
}

func buildTimeSeries(store sampleSource, deployment string, since time.Time) map[string]MetricSeries {
	grouped := map[string]map[string][]Sample{}
	for _, key := range store.Series() {
		if deployment != "" && key.Deployment != deployment {
			continue
		}
		samples := store.Range(key, since)
		if len(samples) == 0 {
			continue
		}
		if grouped[key.Metric] == nil {
			grouped[key.Metric] = map[string][]Sample{}
		}
		grouped[key.Metric][key.Container] = samples
	}

	out := make(map[string]MetricSeries, len(grouped))
	for metric, byContainer := range grouped {
		containers := make([]string, 0, len(byContainer))
		tsSet := map[int64]struct{}{}
		for c, samples := range byContainer {
			containers = append(containers, c)
			for _, s := range samples {
				tsSet[s.Time.Unix()] = struct{}{}
			}
		}
		sort.Strings(containers)
		timestamps := make([]int64, 0, len(tsSet))
		for t := range tsSet {
			timestamps = append(timestamps, t)
		}
		sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })

		values := make([][]*float64, len(containers))
		for ci, c := range containers {
			atTime := make(map[int64]float64, len(byContainer[c]))
			for _, s := range byContainer[c] {
				atTime[s.Time.Unix()] = s.Value
			}
			row := make([]*float64, len(timestamps))
			for ti, t := range timestamps {
				if v, ok := atTime[t]; ok {
					vv := v
					row[ti] = &vv
				}
			}
			values[ci] = row
		}
		out[metric] = MetricSeries{Containers: containers, Timestamps: timestamps, Values: values}
	}
	return out
}
