// Package telemetry configures OpenTelemetry tracing and metrics for mortise.
// If no OTLP endpoint is configured, providers still work but export nowhere,
// so instrumentation code can be unconditional.
package telemetry

import (
	"context"
	"fmt"

	"github.com/mrn-dk/mortise/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Telemetry bundles the tracer and metric instruments mortise emits.
type Telemetry struct {
	Tracer trace.Tracer

	// Instruments recorded per request.
	Requests     metric.Int64Counter
	Errors       metric.Int64Counter
	Retries      metric.Int64Counter
	Duration     metric.Float64Histogram
	PromptTokens metric.Int64Counter
	CompTokens   metric.Int64Counter

	shutdown []func(context.Context) error
}

// Init constructs providers, registers them globally, and builds instruments.
func Init(ctx context.Context, cfg config.Telemetry) (*Telemetry, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	t := &Telemetry{}

	// Tracing.
	var tpOpts []sdktrace.TracerProviderOption
	tpOpts = append(tpOpts, sdktrace.WithResource(res))
	if cfg.OTLPEndpoint != "" {
		texp, err := otlptracegrpc.New(ctx, grpcTraceOpts(cfg)...)
		if err != nil {
			return nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		tpOpts = append(tpOpts, sdktrace.WithBatcher(texp))
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)
	t.shutdown = append(t.shutdown, tp.Shutdown)
	t.Tracer = tp.Tracer("mortise")

	// Metrics.
	var mpOpts []sdkmetric.Option
	mpOpts = append(mpOpts, sdkmetric.WithResource(res))
	if cfg.OTLPEndpoint != "" {
		mexp, err := otlpmetricgrpc.New(ctx, grpcMetricOpts(cfg)...)
		if err != nil {
			return nil, fmt.Errorf("otlp metric exporter: %w", err)
		}
		mpOpts = append(mpOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp)))
	}
	mp := sdkmetric.NewMeterProvider(mpOpts...)
	otel.SetMeterProvider(mp)
	t.shutdown = append(t.shutdown, mp.Shutdown)

	m := mp.Meter("mortise")
	if t.Requests, err = m.Int64Counter("mortise.requests",
		metric.WithDescription("Total chat completion requests")); err != nil {
		return nil, err
	}
	if t.Errors, err = m.Int64Counter("mortise.errors",
		metric.WithDescription("Requests that returned an error to the client")); err != nil {
		return nil, err
	}
	if t.Retries, err = m.Int64Counter("mortise.retries",
		metric.WithDescription("Upstream attempts beyond the first (retries/failover)")); err != nil {
		return nil, err
	}
	if t.Duration, err = m.Float64Histogram("mortise.request.duration",
		metric.WithDescription("End-to-end request duration"), metric.WithUnit("s")); err != nil {
		return nil, err
	}
	if t.PromptTokens, err = m.Int64Counter("mortise.tokens.prompt",
		metric.WithDescription("Prompt tokens billed")); err != nil {
		return nil, err
	}
	if t.CompTokens, err = m.Int64Counter("mortise.tokens.completion",
		metric.WithDescription("Completion tokens billed")); err != nil {
		return nil, err
	}
	return t, nil
}

func grpcTraceOpts(cfg config.Telemetry) []otlptracegrpc.Option {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint)}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	return opts
}

func grpcMetricOpts(cfg config.Telemetry) []otlpmetricgrpc.Option {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint)}
	if cfg.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	return opts
}

// Shutdown flushes and stops all providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var firstErr error
	for _, fn := range t.shutdown {
		if err := fn(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
