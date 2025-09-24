package traces

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	ServiceName      string
	Environment      string
	Secure           bool
	ExporterEndpoint string
}

var once sync.Once

// InitTrace initializes OpenTelemetry tracing with the given configuration.
func InitTrace(cfg Config) (func(context.Context) error, error) {
	// Ensure propagator is set only once
	once.Do(func() {
		otel.SetTextMapPropagator(
			propagation.NewCompositeTextMapPropagator(
				propagation.TraceContext{},
				propagation.Baggage{},
			),
		)
	})
	// Exporter setup (e.g., OTLP exporter)
	traceExporter := newTraceExporter(cfg.ExporterEndpoint, cfg.Secure)
	// Resource setup with service name and environment

	// Tracer provider setup with batch span processor and resource
	tp, err := newTracProvider(cfg, traceExporter)
	if err != nil {
		return nil, err
	}

	// Set global tracer provider
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// GetTracer returns a tracer for the given name.
func GetTracer(name string) trace.Tracer {
	return otel.GetTracerProvider().Tracer(name)
}

// newTraceExporter creates a new OTLP trace exporter.
func newTraceExporter(exporterEndpoint string, secure bool) sdktrace.SpanExporter {
	var opts []otlptracehttp.Option
	if strings.HasPrefix(exporterEndpoint, "https://") || secure {
		opts = append(opts, otlptracehttp.WithEndpoint(strings.TrimPrefix(exporterEndpoint, "https://")))
	} else {
		opts = append(opts, otlptracehttp.WithInsecure(), otlptracehttp.WithEndpoint(strings.TrimPrefix(exporterEndpoint, "http://")))
	}

	exporter, err := otlptrace.New(
		context.Background(),
		otlptracehttp.NewClient(opts...),
	)
	if err != nil {
		return nil
	}
	return exporter
}

// newTracProvider creates a new tracer provider with the given configuration and exporter.
func newTracProvider(cfg Config, exporter sdktrace.SpanExporter) (*sdktrace.TracerProvider, error) {
	// Create resource with service attributes
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
			semconv.ServiceNamespaceKey.String("uber-clone"),
			semconv.HostArchAMD64,
			semconv.OSTypeLinux,
			semconv.TelemetrySDKLanguageGo,
		),
		resource.WithHost(),
		resource.WithOSType(),
		resource.WithTelemetrySDK(),
		resource.WithSchemaURL(semconv.SchemaURL),
	)
	if err != nil {
		return nil, err
	}

	// Create RPC observer for metrics
	// rpcObserver := rpcmetrics.NewObserver(metricsFactory, rpcmetrics.DefaultNameNormalizer)
	// Note: rpcObserver cannot be used as a SpanProcessor for TracerProvider.

	// Create tracer provider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Second)),
	)

	return tp, nil
}
