// Package observ is the FlatRun observability engine: it collects per-container metrics,
// keeps a bounded recent-history window for the native UI, and serves them for scraping in
// Prometheus format so any external tool can consume the same data. Metric names follow the
// OpenTelemetry container semantic conventions.
package observ

import (
	"sort"
	"sync"
	"time"
)

// OpenTelemetry container metric names (semconv). Emitting these verbatim keeps FlatRun's
// metrics interoperable with any OTel backend.
const (
	MetricCPUUsage    = "container.cpu.usage"
	MetricMemoryUsage = "container.memory.usage"
	MetricMemoryLimit = "container.memory.limit"
	MetricNetworkRx   = "container.network.io.rx"
	MetricNetworkTx   = "container.network.io.tx"
)

// Sample is a single metric reading at a point in time.
type Sample struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// SeriesKey identifies one metric stream: a metric for a container within a deployment.
type SeriesKey struct {
	Deployment string
	Container  string
	Metric     string
}

// ContainerSample is the raw per-container reading the scraper feeds in; the store expands
// it into the individual semconv metric series.
type ContainerSample struct {
	Deployment  string
	Container   string
	CPUPercent  float64
	MemoryUsage uint64
	MemoryLimit uint64
	NetworkRx   uint64
	NetworkTx   uint64
}

// Store holds a bounded ring of recent samples per series, so the UI can render recent
// history without an external time-series database. Older samples are discarded once the
// per-series capacity is reached.
type Store struct {
	mu        sync.RWMutex
	capacity  int
	retention time.Duration
	series    map[SeriesKey]*ring
	lastSweep time.Time
}

func NewStore(capacityPerSeries int) *Store {
	if capacityPerSeries <= 0 {
		capacityPerSeries = 720 // e.g. 1h at 5s resolution
	}
	return &Store{
		capacity:  capacityPerSeries,
		retention: time.Hour,
		series:    make(map[SeriesKey]*ring),
	}
}

// sweepStale drops series whose newest sample has aged past the retention window, so a
// container that stops reporting (removed, deployment torn down) does not keep its ring alive
// for the process lifetime. The caller must hold s.mu. It runs at most once per retention
// window using the sample clock, keeping it O(series) amortized.
func (s *Store) sweepStale(now time.Time) {
	if s.retention <= 0 {
		return
	}
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < s.retention {
		return
	}
	s.lastSweep = now
	cutoff := now.Add(-s.retention)
	for k, r := range s.series {
		if last, ok := r.last(); ok && last.Time.Before(cutoff) {
			delete(s.series, k)
		}
	}
}

func (s *Store) add(key SeriesKey, sample Sample) {
	r := s.series[key]
	if r == nil {
		r = newRing(s.capacity)
		s.series[key] = r
	}
	r.push(sample)
}

// Record expands a container reading into its semconv series and stores each at t.
func (s *Store) Record(c ContainerSample, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	base := SeriesKey{Deployment: c.Deployment, Container: c.Container}
	for metric, value := range map[string]float64{
		MetricCPUUsage:    c.CPUPercent,
		MetricMemoryUsage: float64(c.MemoryUsage),
		MetricMemoryLimit: float64(c.MemoryLimit),
		MetricNetworkRx:   float64(c.NetworkRx),
		MetricNetworkTx:   float64(c.NetworkTx),
	} {
		key := base
		key.Metric = metric
		s.add(key, Sample{Time: t, Value: value})
	}
	s.sweepStale(t)
}

// Range returns the samples for a series recorded at or after since, oldest first.
func (s *Store) Range(key SeriesKey, since time.Time) []Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.series[key]
	if r == nil {
		return nil
	}
	return r.since(since)
}

// LatestPoint is a series' most recent sample.
type LatestPoint struct {
	SeriesKey
	Sample
}

// Latest returns the most recent sample of every series, sorted by key.
func (s *Store) Latest() []LatestPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LatestPoint, 0, len(s.series))
	for k, r := range s.series {
		if last, ok := r.last(); ok {
			out = append(out, LatestPoint{SeriesKey: k, Sample: last})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Deployment != out[j].Deployment {
			return out[i].Deployment < out[j].Deployment
		}
		if out[i].Container != out[j].Container {
			return out[i].Container < out[j].Container
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

// Series lists the keys currently held, so callers can enumerate what is available.
func (s *Store) Series() []SeriesKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]SeriesKey, 0, len(s.series))
	for k := range s.series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Deployment != keys[j].Deployment {
			return keys[i].Deployment < keys[j].Deployment
		}
		if keys[i].Container != keys[j].Container {
			return keys[i].Container < keys[j].Container
		}
		return keys[i].Metric < keys[j].Metric
	})
	return keys
}

// ring is a fixed-capacity circular buffer of samples in insertion order.
type ring struct {
	buf   []Sample
	next  int
	count int
}

func newRing(capacity int) *ring {
	return &ring{buf: make([]Sample, capacity)}
}

func (r *ring) push(s Sample) {
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

// ordered returns the buffered samples oldest-first.
func (r *ring) ordered() []Sample {
	out := make([]Sample, 0, r.count)
	start := 0
	if r.count == len(r.buf) {
		start = r.next
	}
	for i := 0; i < r.count; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

func (r *ring) last() (Sample, bool) {
	if r.count == 0 {
		return Sample{}, false
	}
	return r.buf[(r.next-1+len(r.buf))%len(r.buf)], true
}

func (r *ring) since(t time.Time) []Sample {
	all := r.ordered()
	idx := sort.Search(len(all), func(i int) bool { return !all[i].Time.Before(t) })
	return all[idx:]
}
