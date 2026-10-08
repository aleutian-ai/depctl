// Package trace wraps OpenTelemetry span creation in a one-liner helper
// (OBS-002), off by default: with no provider configured, otel's own
// global tracer is already a no-op, so a disabled config compiles in zero
// export path — no background exporter goroutine, no network call.
package trace

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/aleutian-ai/depctl/internal/config"
)

// tracerName is the one instrumentation-scope name every depctl span is
// recorded under; call sites never construct their own tracer.
const tracerName = "github.com/aleutian-ai/depctl"

// InitProvider wires the global tracer provider from cfg. When
// cfg.Enabled is false (the default), it does nothing — otel.Tracer calls
// then resolve to the SDK's own built-in no-op provider, so tracing has
// zero runtime cost and touches the network never. When enabled, it
// registers a real batched OTLP/HTTP exporter and returns a shutdown func
// the caller must invoke on process exit to flush pending spans.
//
// A failed exporter connection is not treated as an init error, per this
// ticket's own failure-behavior requirement (tracing must never block
// startup or operation correctness): logger receives one warning, and the
// provider is still installed — the exporter itself retries/degrades
// internally, same as any OTLP exporter would against a Jaeger/Tempo/
// collector that comes and goes.
func InitProvider(ctx context.Context, cfg config.OTelConfig, logger *slog.Logger) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if !cfg.Enabled {
		return noop, nil
	}

	opts, err := exporterOptions(cfg.Endpoint)
	if err != nil {
		if logger != nil {
			logger.Warn("otel: invalid observability.otel.endpoint, tracing disabled for this run", "error", err)
		}
		return noop, nil
	}
	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		if logger != nil {
			logger.Warn("otel: exporter init failed, tracing disabled for this run", "error", err)
		}
		return noop, nil
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("depctl")))
	if err != nil {
		res = resource.Default()
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)

	return provider.Shutdown, nil
}

// exporterOptions turns cfg's endpoint (a plain "host:port", or a full
// "http(s)://host:port" URL — either is a reasonable thing for an operator
// to write in config.yaml) into otlptracehttp options built on
// WithEndpoint rather than WithEndpointURL: WithEndpointURL takes the URL
// as the literal request target and never appends a path, while
// WithEndpoint always appends the standard "/v1/traces" path itself — the
// same behavior OTEL_EXPORTER_OTLP_ENDPOINT has in every other OTel SDK,
// so a plain host:port in config.yaml just works against a real
// collector/Jaeger/Tempo without the operator needing to know OTLP's own
// URL-path convention.
func exporterOptions(endpoint string) ([]otlptracehttp.Option, error) {
	host := endpoint
	insecure := true
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host = u.Host
		insecure = u.Scheme != "https"
	} else if endpoint == "" {
		return nil, fmt.Errorf("empty endpoint")
	}
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(host)}
	if insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	return opts, nil
}

// StartSpan starts a span named name as a child of ctx, returning the new
// context to pass to downstream calls and a func to end the span — the
// one-liner every pipeline-stage call site uses instead of touching the
// OTel SDK directly:
//
//	ctx, end := trace.StartSpan(ctx, "embed")
//	defer end()
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func()) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, name, oteltrace.WithAttributes(attrs...))
	return ctx, func() { span.End() }
}

// RecordError records err on ctx's current span (a no-op if err is nil) —
// used wherever a stage's own error is known at the point tracing ends:
//
//	ctx, end := trace.StartSpan(ctx, "embed")
//	defer func() { end() }()
//	...
//	if err != nil {
//	    trace.RecordError(ctx, err)
//	    return err
//	}
func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	span := oteltrace.SpanFromContext(ctx)
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
