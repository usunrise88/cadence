package obs

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Metrics are the control plane's Prometheus series, on a registry of their own.
type Metrics struct {
	Registry         *prometheus.Registry
	HTTPDuration     *prometheus.HistogramVec // route, method, status
	SSEClients       prometheus.Gauge
	EventsDispatched prometheus.Counter
	Commands         *prometheus.CounterVec // operation, outcome
}

// NewMetrics registers the series and the Go and process collectors.
func NewMetrics() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "cadence_http_request_duration_seconds",
			Help:    "HTTP request duration by route pattern, method and status.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method", "status"}),
		SSEClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cadence_sse_clients",
			Help: "Connected event-stream clients.",
		}),
		EventsDispatched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cadence_events_dispatched_total",
			Help: "Outbox events published to live subscribers.",
		}),
		Commands: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cadence_commands_total",
			Help: "Commands by operation and outcome (ok, dry_run, replayed, or the problem slug).",
		}, []string{"operation", "outcome"}),
	}
	m.Registry.MustRegister(m.HTTPDuration, m.SSEClients, m.EventsDispatched, m.Commands,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}

// Trace wraps h so every request (except /healthz and /metrics) runs in a server span that continues an incoming
// W3C traceparent.
func Trace(h http.Handler, tp trace.TracerProvider) http.Handler {
	return otelhttp.NewHandler(h, "http",
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithPropagators(Propagator()),
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" && r.URL.Path != "/metrics" }),
	)
}

// RequestLog logs one line per request (method, route pattern, status, duration, trace_id), observes the duration
// histogram and names the span after the route. Mount it on the root chi router, inside Trace.
func RequestLog(log *slog.Logger, m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			took := time.Since(start)

			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			m.HTTPDuration.WithLabelValues(route, r.Method, strconv.Itoa(status)).Observe(took.Seconds())
			span := trace.SpanFromContext(r.Context())
			span.SetName(r.Method + " " + route)
			span.SetAttributes(attribute.String("http.route", route))

			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case route == "/healthz" || route == "/metrics":
				level = slog.LevelDebug
			}
			log.LogAttrs(r.Context(), level, "http",
				slog.String("method", r.Method), slog.String("route", route), slog.String("path", r.URL.Path),
				slog.Int("status", status), slog.Int64("duration_ms", took.Milliseconds()),
				slog.Int("bytes", ww.BytesWritten()))
		})
	}
}
