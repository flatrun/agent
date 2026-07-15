package observ

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOTLPExportReachesABackend stands up an OTLP/HTTP receiver and checks FlatRun actually
// pushes to it. The point is the wire: the README promises an OpenTelemetry backend can read
// these metrics, and only a real request proves that.
func TestOTLPExportReachesABackend(t *testing.T) {
	var (
		mu       sync.Mutex
		gotPath  string
		gotType  string
		gotBody  []byte
		received = make(chan struct{}, 1)
	)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotType, gotBody = r.URL.Path, r.Header.Get("Content-Type"), body
		mu.Unlock()
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	store := NewStore(10)
	store.Record(ContainerSample{Deployment: "shop", Container: "shop-web", CPUPercent: 42, MemoryUsage: 2048}, time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdown, err := StartOTLPExport(ctx, store, backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Shutdown flushes rather than waiting out the export interval.
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer flushCancel()
	if err := shutdown(flushCtx); err != nil {
		t.Fatalf("flush to the backend failed: %v", err)
	}

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("no metrics reached the backend")
	}

	mu.Lock()
	defer mu.Unlock()

	if gotPath != "/v1/metrics" {
		t.Errorf("posted to %q, want the OTLP metrics path /v1/metrics", gotPath)
	}
	if gotType != "application/x-protobuf" {
		t.Errorf("content-type = %q, want OTLP protobuf", gotType)
	}
	if len(gotBody) == 0 {
		t.Fatal("posted an empty body")
	}

	// The payload is protobuf, but the metric names and attributes travel as plain strings
	// inside it, so this confirms the right series went out under their semconv names.
	body := string(gotBody)
	for _, want := range []string{MetricCPUUsage, MetricMemoryUsage, "deployment", "shop", "shop-web"} {
		if !strings.Contains(body, want) {
			t.Errorf("payload missing %q", want)
		}
	}
}

func TestOTLPExporterPicksProtocolFromEndpoint(t *testing.T) {
	ctx := context.Background()

	// A URL means OTLP/HTTP; a bare host:port is the gRPC convention. Neither should dial
	// at construction, so this only checks the choice is made without erroring.
	for _, endpoint := range []string{"http://localhost:4318", "https://otel.example:4318", "localhost:4317"} {
		exp, err := newOTLPExporter(ctx, endpoint)
		if err != nil {
			t.Errorf("endpoint %q: %v", endpoint, err)
			continue
		}
		_ = exp.Shutdown(ctx)
	}
}

func TestMetricUnits(t *testing.T) {
	// A backend renders bytes as bytes only if told the unit, so every series carries one.
	for _, name := range []string{MetricMemoryUsage, MetricMemoryLimit, MetricNetworkRx, MetricNetworkTx} {
		if got := metricUnit(name); got != "By" {
			t.Errorf("metricUnit(%q) = %q, want By", name, got)
		}
	}
	if got := metricUnit(MetricCPUUsage); got != "%" {
		t.Errorf("cpu unit = %q, want %%", got)
	}
}
