// Package obs sets up logs, metrics and traces: JSON slog lines to stdout and a rotating file, Prometheus metrics
// on a private registry, and OpenTelemetry spans written as JSON lines to a rotating file.
package obs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Retention of rotated log and trace files (docs/spec/06-platform.md, "Retention").
const retentionDays = 14

func rotating(dir, name string) *lumberjack.Logger {
	return &lumberjack.Logger{Filename: filepath.Join(dir, name), MaxSize: 100, MaxAge: retentionDays, Compress: true}
}

// NewLogger returns a JSON logger writing to stdout and $dir/cadence.log (rotated at 100 MB, kept 14 days). Every
// line logged with a context that carries a span gets trace_id and span_id.
func NewLogger(dir string, level slog.Level) (*slog.Logger, io.Closer, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}
	file := rotating(dir, "cadence.log")
	h := slog.NewJSONHandler(io.MultiWriter(os.Stdout, file), &slog.HandlerOptions{Level: level})
	return slog.New(traceHandler{h}), file, nil
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(as)}
}
func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// Propagator carries W3C traceparent/tracestate, so a trace started in the UI continues in the API.
func Propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
}

// Traceparent renders the span context ctx carries as a W3C traceparent; "" when it carries none. Jobs keep it so
// their spans continue the request that started them, and step leases hand it to the worker.
func Traceparent(ctx context.Context) string {
	c := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, c)
	return c.Get("traceparent")
}

// WithTraceparent returns ctx carrying traceparent as its remote parent span; ctx itself when tp is empty or invalid.
func WithTraceparent(ctx context.Context, tp string) context.Context {
	if tp == "" {
		return ctx
	}
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": tp})
}

// NewTracerProvider exports every span as one JSON line to $dir/traces.jsonl (rotated like the log).
// Shut it down to flush.
func NewTracerProvider(dir, version string) (*sdktrace.TracerProvider, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create trace dir %s: %w", dir, err)
	}
	exp, err := stdouttrace.New(stdouttrace.WithWriter(rotating(dir, "traces.jsonl")))
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", "cadence-control-plane"),
		attribute.String("service.version", version),
	)
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res)), nil
}
