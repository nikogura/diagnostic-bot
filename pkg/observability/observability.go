// Package observability wires the three OpenTelemetry signals for the service:
// metrics exported in Prometheus format, distributed tracing over OTLP, and
// W3C trace-context propagation. Tracing is no-op when no OTLP endpoint is
// configured, so the service runs identically with or without a collector.
package observability

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// Providers bundles the configured OTel providers plus the Prometheus registry
// the metrics endpoint must serve. Call Shutdown on graceful exit.
type Providers struct {
	// Registry is the Prometheus registry the OTel metrics exporter writes to;
	// the metrics HTTP server serves it via promhttp.
	Registry *prometheus.Registry

	shutdownFuncs []func(ctx context.Context) (err error)
	tracingActive bool
}

// TracingActive reports whether an OTLP trace exporter was configured.
func (p *Providers) TracingActive() (active bool) {
	active = p.tracingActive
	return active
}

// Shutdown flushes and stops every configured provider, returning the first
// error encountered (after attempting them all).
func (p *Providers) Shutdown(ctx context.Context) (err error) {
	for _, fn := range p.shutdownFuncs {
		shutdownErr := fn(ctx)
		if shutdownErr != nil && err == nil {
			err = shutdownErr
		}
	}

	return err
}

// Init configures the global OTel meter and tracer providers and the text-map
// propagator, returning the Prometheus registry to serve and a Providers handle
// for shutdown.
func Init(ctx context.Context, serviceName string, version string, logger *slog.Logger) (providers *Providers, err error) {
	var res *resource.Resource

	res, err = buildResource(serviceName, version)
	if err != nil {
		err = fmt.Errorf("building otel resource: %w", err)
		return providers, err
	}

	registry := prometheus.NewRegistry()

	var meterProvider *sdkmetric.MeterProvider

	meterProvider, err = buildMeterProvider(registry, res)
	if err != nil {
		err = fmt.Errorf("building meter provider: %w", err)
		return providers, err
	}

	otel.SetMeterProvider(meterProvider)

	var tracerProvider *sdktrace.TracerProvider

	var tracingActive bool

	tracerProvider, tracingActive, err = buildTracerProvider(ctx, res, logger)
	if err != nil {
		err = fmt.Errorf("building tracer provider: %w", err)
		return providers, err
	}

	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	providers = &Providers{
		Registry:      registry,
		shutdownFuncs: []func(context.Context) error{meterProvider.Shutdown, tracerProvider.Shutdown},
		tracingActive: tracingActive,
	}

	logger.InfoContext(ctx, "observability initialized",
		slog.String("service", serviceName),
		slog.Bool("tracing_active", tracingActive))

	return providers, err
}

// buildResource assembles the OTel resource describing this service.
func buildResource(serviceName string, version string) (res *resource.Resource, err error) {
	res, err = resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)

	return res, err
}

// buildMeterProvider creates a meter provider whose metrics are exported in
// Prometheus format. Counter/unit suffixes are disabled so instrument names map
// 1:1 to the metric names operators query.
func buildMeterProvider(registry *prometheus.Registry, res *resource.Resource) (provider *sdkmetric.MeterProvider, err error) {
	exporter, newErr := promexporter.New(
		promexporter.WithRegisterer(registry),
		// Map instrument names 1:1 to metric names (no unit/_total suffixes) so
		// dashboards and alerts reference exactly the names we declare.
		promexporter.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithoutSuffixes),
		promexporter.WithoutScopeInfo(),
	)
	if newErr != nil {
		err = newErr
		return provider, err
	}

	provider = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(exporter),
		// Latency is an SLO budget, so give the investigation-duration histogram
		// boundaries in seconds tuned to real investigation times rather than
		// the SDK's millisecond-scale defaults.
		sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Name: "investigation_duration_seconds"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
				Boundaries: []float64{1, 2, 5, 10, 30, 60, 120, 300, 600},
			}},
		)),
	)

	return provider, err
}

// buildTracerProvider creates a tracer provider that exports over OTLP/HTTP when
// an endpoint is configured, and is otherwise a cheap no-op (NeverSample, no
// processor) so the service behaves identically without a collector.
func buildTracerProvider(ctx context.Context, res *resource.Resource, logger *slog.Logger) (provider *sdktrace.TracerProvider, active bool, err error) {
	endpoint := otlpEndpoint()
	if endpoint == "" {
		provider = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.NeverSample()),
		)

		logger.InfoContext(ctx, "OTLP trace endpoint unset — tracing runs no-op")

		return provider, active, err
	}

	exporter, newErr := otlptracehttp.New(ctx)
	if newErr != nil {
		err = fmt.Errorf("creating OTLP trace exporter: %w", newErr)
		return provider, active, err
	}

	provider = sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	active = true

	logger.InfoContext(ctx, "OTLP tracing enabled", slog.String("endpoint", endpoint))

	return provider, active, err
}

// otlpEndpoint returns the configured OTLP endpoint, honoring both the
// traces-specific and the general OTLP environment variables.
func otlpEndpoint() (endpoint string) {
	endpoint = os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	return endpoint
}
