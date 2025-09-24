package observe

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type Config struct {
	ServiceName      string
	Environment      string
	Secure           bool
	ExporterEndpoint string
}

func InitMetric(cfg Config) (func(context.Context) error, error) {
	// Exporter setup (e.g., OTLP exporter)
	metricExporter := newMetricExporter(cfg.ExporterEndpoint, cfg.Secure)
	// Resource setup with service name and environment

	// Meter provider setup with periodic reader and resource
	mp, err := newMeterProvider(cfg, metricExporter)
	if err != nil {
		return nil, err
	}

	otel.SetMeterProvider(mp)
	return mp.Shutdown, nil
}

func InitLog(cfg Config) (func(context.Context) error, error) {
	// Exporter setup (e.g., OTLP exporter)
	logExporter := newLogExporter(cfg.ExporterEndpoint, cfg.Secure)
	// Resource setup with service name and environment

	// Logger provider setup with batch processor and resource
	lp, err := newLogProvider(cfg, logExporter)
	if err != nil {
		return nil, err
	}

	global.SetLoggerProvider(lp)
	return lp.Shutdown, nil
}

func newMetricExporter(exporterEndpoint string, secure bool) metric.Exporter {
	var opts []otlpmetrichttp.Option
	if strings.HasPrefix(exporterEndpoint, "https://") || secure {
		opts = append(opts, otlpmetrichttp.WithEndpoint(strings.TrimPrefix(exporterEndpoint, "https://")))
	} else {
		opts = append(opts, otlpmetrichttp.WithInsecure(), otlpmetrichttp.WithEndpoint(strings.TrimPrefix(exporterEndpoint, "http://")))
	}

	exporter, err := otlpmetrichttp.New(context.Background(), opts...)
	if err != nil {
		return nil
	}
	return exporter
}

func newMeterProvider(cfg Config, exporter metric.Exporter) (*metric.MeterProvider, error) {
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
			semconv.ServiceNamespaceKey.String("uber-clone"),
			semconv.HostArchAMD64,
			semconv.OSTypeLinux),
		resource.WithHost(),
		resource.WithOSType(),
		resource.WithTelemetrySDK(),
		resource.WithSchemaURL(semconv.SchemaURL),
	)
	if err != nil {
		return nil, err
	}

	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(exporter)),
		metric.WithResource(res),
	)

	return mp, nil
}

func newLogExporter(exporterEndpoint string, secure bool) log.Exporter {
	var opts []otlploghttp.Option
	if strings.HasPrefix(exporterEndpoint, "https://") || secure {
		opts = append(opts, otlploghttp.WithEndpoint(strings.TrimPrefix(exporterEndpoint, "https://")))
	} else {
		opts = append(opts, otlploghttp.WithInsecure(), otlploghttp.WithEndpoint(strings.TrimPrefix(exporterEndpoint, "http://")))
	}

	exporter, err := otlploghttp.New(context.Background(), opts...)
	if err != nil {
		return nil
	}
	return exporter
}

func newLogProvider(cfg Config, exporter log.Exporter) (*log.LoggerProvider, error) {
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
			semconv.ServiceNamespaceKey.String("uber-clone"),
			semconv.HostArchAMD64,
			semconv.OSTypeLinux),
		resource.WithHost(),
		resource.WithOSType(),
		resource.WithTelemetrySDK(),
		resource.WithSchemaURL(semconv.SchemaURL),
	)
	if err != nil {
		return nil, err
	}

	processor := log.NewBatchProcessor(exporter)
	lp := log.NewLoggerProvider(log.WithProcessor(processor), log.WithResource(res))

	return lp, nil
}
