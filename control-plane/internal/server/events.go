package server

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
)

// eventStream is the generated (non-strict) interface with events.list overridden: a request that accepts
// text/event-stream gets the live SSE stream, which the strict layer cannot express; any other request falls
// through to the strict handler's JSON page.
type eventStream struct {
	api.ServerInterface
	s *Server
}

// EventsList serves events.list as SSE or delegates to the JSON form.
func (e eventStream) EventsList(w http.ResponseWriter, r *http.Request, params api.EventsListParams) {
	if !acceptsEventStream(r.Header.Get("Accept")) {
		e.ServerInterface.EventsList(w, r, params)
		return
	}
	q, err := e.s.eventQuery(r.Context(), params)
	if err != nil {
		e.s.writeProblem(w, r, err)
		return
	}
	e.s.Streamer.Serve(w, r, q.filter, q.after, q.resume)
}

func acceptsEventStream(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		if mt, _, err := mime.ParseMediaType(strings.TrimSpace(part)); err == nil && mt == "text/event-stream" {
			return true
		}
	}
	return false
}

type eventQuery struct {
	filter events.Filter
	after  int64
	resume bool // after was given (query or Last-Event-ID); otherwise the stream starts live
	limit  int
}

// eventQuery reads the shared parameters. Last-Event-ID wins over ?after: a reconnecting EventSource repeats
// its original URL but sends the id it last saw. project accepts a slug or a prj_ id.
func (s *Server) eventQuery(ctx context.Context, p api.EventsListParams) (eventQuery, error) {
	q := eventQuery{limit: 200}
	if p.Limit != nil {
		q.limit = *p.Limit
	}
	if p.After != nil {
		q.after, q.resume = *p.After, true
	}
	if p.LastEventID != nil && strings.TrimSpace(*p.LastEventID) != "" {
		seq, err := strconv.ParseInt(strings.TrimSpace(*p.LastEventID), 10, 64)
		if err != nil || seq < 0 {
			return q, problems.BadRequest.New("Last-Event-ID %q is not an event seq", *p.LastEventID)
		}
		q.after, q.resume = seq, true
	}
	project := deref(p.Project)
	if project != "" && !strings.HasPrefix(project, "prj_") {
		pr, err := projects.Get(ctx, s.Pool, project)
		if err != nil {
			return q, err
		}
		project = pr.ID
	}
	// A scoped credential sees its own project's events (and registry events) only.
	scope, _ := auth.ScopeFromContext(ctx)
	if !scope.All {
		switch {
		case scope.ProjectID == "":
			return q, problems.Forbidden.New("this credential reaches no project; the event stream is filtered per project")
		case project == "":
			project = scope.ProjectID
		case project != scope.ProjectID:
			return q, problems.Forbidden.New("this credential does not reach project %s", deref(p.Project))
		}
	}
	f, err := events.ParseFilter(deref(p.Topics), project)
	if err != nil {
		return q, problems.BadRequest.New("%v", err)
	}
	q.filter = f
	return q, nil
}

// EventsList implements the JSON form of events.list: a page of events after `after`, oldest first.
func (s *Server) EventsList(ctx context.Context, req api.EventsListRequestObject) (api.EventsListResponseObject, error) {
	q, err := s.eventQuery(ctx, req.Params)
	if err != nil {
		return nil, err
	}
	recs, err := events.List(ctx, s.Pool, q.filter, q.after, q.limit)
	if err != nil {
		return nil, err
	}
	out := api.EventList{Items: make([]api.CadenceEvent, 0, len(recs)), LastSeq: q.after}
	for _, r := range recs {
		ev, err := apiEvent(r)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, ev)
		out.LastSeq = r.Seq
	}
	return api.EventsList200JSONResponse(out), nil
}

// apiEvent converts through JSON: events.Record's JSON form is the contract's CadenceEvent.
func apiEvent(r events.Record) (api.CadenceEvent, error) {
	var ev api.CadenceEvent
	b, err := json.Marshal(r)
	if err == nil {
		err = json.Unmarshal(b, &ev)
	}
	if err != nil {
		return api.CadenceEvent{}, fmt.Errorf("convert event %d: %w", r.Seq, err)
	}
	return ev, nil
}
