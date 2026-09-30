package server

import (
	"bytes"
	"context"
	"net/http"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cli"
	"github.com/usunrise88/cadence/control-plane/internal/contract"
)

// Reads a playbook chain waits on (jobs.wait, checkpoints.list) tick the plan of a playbook session like its commands
// do, but reads never pass the command pipeline: observeReads records the response of an agent session's read and,
// once the strict handler has named the operation (nameOperation), hands it to the playbook service.

type opKey struct{}

// opName carries the operation id from the strict handler back out to observeReads.
type opName struct{ id string }

// operationIDs maps the strict handler's operation names (oapi-codegen's Go names: RunsGet) to operation ids (runs.get).
var operationIDs = func() map[string]string {
	m := make(map[string]string, len(cli.Operations))
	for _, o := range cli.Operations {
		m[contract.GoName(o.ID)] = o.ID
	}
	return m
}()

// nameOperation is a strict middleware: it names the operation of the request for observeReads.
func nameOperation(f api.StrictHandlerFunc, operationID string) api.StrictHandlerFunc {
	id := operationIDs[operationID]
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		if n, ok := ctx.Value(opKey{}).(*opName); ok {
			n.id = id
		}
		return f(ctx, w, r, request)
	}
}

// maxObserved caps the response body kept for a playbook plan (a job or a checkpoint list).
const maxObserved = 1 << 20

// recorder keeps the status and the start of the body of a response while it is written.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if room := maxObserved - r.body.Len(); room > 0 {
		r.body.Write(b[:min(len(b), room)])
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// observeReads records the reads of agent sessions a playbook chain names; commands tick through the pipeline.
func (s *Server) observeReads(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.FromContext(r.Context())
		if !ok || actor.SessionID == "" || s.playbooks == nil {
			next.ServeHTTP(w, r)
			return
		}
		name := &opName{}
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), opKey{}, name)))
		if name.id == "" || rec.status < 200 || rec.status > 299 || !s.playbooks.Reads(name.id) {
			return
		}
		s.playbooks.Observed(context.WithoutCancel(r.Context()), actor, name.id, rec.status, rec.body.Bytes())
	})
}
