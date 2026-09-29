// Cadence control plane — stub for spike A4 (outbox → SSE → cache patching).
// Real implementation follows the OpenAPI contract in api/openapi.yaml via generated stubs.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

var seq atomic.Int64

type event struct {
	Seq       int64  `json:"seq"`
	Topic     string `json:"topic"`
	Type      string `json:"type"`
	ProjectID string `json:"projectId"`
	At        string `json:"at"`
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	// Spike A4 stub: emits a heartbeat event every 2 s; the real dispatcher reads the outbox table in seq order
	// and honours Last-Event-ID for resume.
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		log.Printf("events: client connected, Last-Event-ID=%q", r.Header.Get("Last-Event-ID"))
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-t.C:
				e := event{Seq: seq.Add(1), Topic: "system", Type: "heartbeat", ProjectID: "-", At: time.Now().UTC().Format(time.RFC3339)}
				b, _ := json.Marshal(e)
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, b)
				flusher.Flush()
			}
		}
	})

	log.Println("cadence control plane stub on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
