package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTraceparentContinuesAndLogsCarryTraceID(t *testing.T) {
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	var logs bytes.Buffer
	log := slog.New(traceHandler{slog.NewJSONHandler(&logs, nil)})
	m := NewMetrics()

	r := chi.NewRouter()
	r.Use(RequestLog(log, m))
	r.Get("/api/projects/{p}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := Trace(r, tp)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/demo", nil)
	req.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}
	if got := spans[0].SpanContext.TraceID().String(); got != traceID {
		t.Errorf("span trace id %s, want the incoming %s", got, traceID)
	}
	if spans[0].Name != "GET /api/projects/{p}" {
		t.Errorf("span name %q", spans[0].Name)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &line); err != nil {
		t.Fatalf("log %q: %v", logs.String(), err)
	}
	if line["trace_id"] != traceID || line["route"] != "/api/projects/{p}" || line["status"] != float64(418) {
		t.Errorf("log line %v", line)
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if len(exp.GetSpans()) != 1 {
		t.Error("/healthz should not be traced")
	}
	var metrics strings.Builder
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metrics.WriteString(rec.Body.String())
	if !strings.Contains(metrics.String(), `cadence_http_request_duration_seconds_count{method="GET",route="/api/projects/{p}",status="418"} 1`) {
		t.Errorf("histogram missing:\n%s", metrics.String())
	}
}
