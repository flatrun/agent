package observ

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// otlpExportInterval is how often collected metrics are pushed. It is deliberately slower
// than the sample interval: a backend wants a steady trickle, not every scrape.
const otlpExportInterval = 30 * time.Second

// StartOTLPExport pushes the collected metrics to an OTLP endpoint until ctx is done, and
// returns a shutdown that flushes what is pending.
//
// The metrics are read from the same store the built-in UI draws, so an external backend and
// FlatRun's own views can never disagree about what a container did.
//
// Values are reported as observable gauges read at export time rather than pushed on every
// sample, which is what lets the export interval differ from the sample interval without
// either side having to buffer.
func StartOTLPExport(ctx context.Context, store *Store, endpoint string) (func(context.Context) error, error) {
	exporter, err := newOTLPExporter(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", "flatrun"),
	))
	if err != nil {
		return nil, fmt.Errorf("failed to describe this agent to the backend: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(otlpExportInterval))),
	)

	if err := registerGauges(provider.Meter("flatrun/observability"), store); err != nil {
		_ = provider.Shutdown(ctx)
		return nil, err
	}

	return provider.Shutdown, nil
}

// newOTLPExporter builds the exporter for the endpoint's scheme. An http or https endpoint
// speaks OTLP/HTTP; anything else is taken as OTLP/gRPC, which is the protocol's default and
// how a bare host:port is conventionally written.
func newOTLPExporter(ctx context.Context, endpoint string) (sdkmetric.Exporter, error) {
	endpoint = strings.TrimSpace(endpoint)

	switch {
	case strings.HasPrefix(endpoint, "http://"), strings.HasPrefix(endpoint, "https://"):
		return otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(endpoint))
	case endpoint != "":
		return otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure())
	default:
		// No endpoint configured here: fall back to the SDK's own environment variables so
		// OTEL_EXPORTER_OTLP_ENDPOINT works as it does for any other OTel program.
		return otlpmetrichttp.New(ctx)
	}
}

// registerGauges wires each stored series to an observable gauge under its semconv name.
func registerGauges(meter metric.Meter, store *Store) error {
	for _, name := range []string{
		MetricCPUUsage,
		MetricMemoryUsage,
		MetricMemoryLimit,
		MetricMemoryUtilization,
		MetricNetworkRx,
		MetricNetworkTx,
	} {
		metricName := name
		opts := []metric.Float64ObservableGaugeOption{
			metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
				for _, p := range store.Latest() {
					if p.Metric != metricName {
						continue
					}
					o.Observe(p.Value, metric.WithAttributes(
						attribute.String("deployment", p.Deployment),
						attribute.String("container.name", p.Container),
					))
				}
				return nil
			}),
		}
		if help, ok := metricHelp[metricName]; ok {
			opts = append(opts, metric.WithDescription(help))
		}
		if unit := metricUnit(metricName); unit != "" {
			opts = append(opts, metric.WithUnit(unit))
		}

		if _, err := meter.Float64ObservableGauge(metricName, opts...); err != nil {
			return fmt.Errorf("failed to register %s: %w", metricName, err)
		}
	}
	return nil
}

// metricUnit gives each series its UCUM unit, which is how an OTel backend knows to render
// bytes as bytes rather than a bare number.
func metricUnit(name string) string {
	switch name {
	case MetricCPUUsage, MetricMemoryUtilization:
		return "%"
	case MetricMemoryUsage, MetricMemoryLimit, MetricNetworkRx, MetricNetworkTx:
		return "By"
	}
	return ""
}
