package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/transcriptions"
)

// ---------------------------------------------------------------- transcriptions and the live channel (phase 3 · stream T)
//
// transcriptions.new is a command (tag media, people only); the two sockets are served by mediaRoutes at the
// non-strict layer: stream.connect (the browser, ticket + Origin) and workerLive.connect (the worker's dial-out, the
// lease's live token or its cwk_ credential). The relay itself is internal/transcriptions.

func (c commandResponse) VisitTranscriptionsNewResponse(w http.ResponseWriter) error {
	return c.write(w)
}

func (s *Server) newTranscriptions() *transcriptions.Service {
	t := &transcriptions.Service{Pool: s.Pool, Jobs: s.Jobs, Leases: s.Leases, CAS: s.CAS, Defaults: s.defaultsDoc, Log: s.Log,
		AllowedOrigins: s.AllowedOrigins}
	if s.Projects != nil {
		t.Repo = s.Projects.Repos()
	}
	if s.Workers != nil {
		t.Wake = s.Workers.Wake
		s.Workers.OnGranted(t.Granted) // an interactive lease carries a fresh live token
	}
	return t
}

// SweepTranscriptions ends sessions nothing serves any more (periodic, every 30 s).
func (s *Server) SweepTranscriptions(ctx context.Context) error {
	_, err := s.transcriptions.Sweep(ctx)
	return err
}

// CloseLive ends every live socket; call it before the HTTP server's shutdown.
func (s *Server) CloseLive() { s.transcriptions.Close() }

// Transcriptions is the live channel's service (tests set its timing).
func (s *Server) Transcriptions() *transcriptions.Service { return s.transcriptions }

// TranscriptionsNew implements transcriptions.new: a manual test session with its socket URL and single-use ticket.
func (s *Server) TranscriptionsNew(ctx context.Context, req api.TranscriptionsNewRequestObject) (api.TranscriptionsNewResponseObject, error) {
	if _, err := mediaViewer(ctx); err != nil {
		return nil, err
	}
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	b := req.Body
	in := transcriptions.NewInput{ProjectID: p.ID, Input: transcriptions.Input{Kind: string(b.Input.Kind), UtteranceID: deref(b.Input.UtteranceId),
		Start: b.Input.Start, End: b.Input.End, Channel: b.Input.Channel}, Blind: b.Blind != nil && *b.Blind}
	if b.Pace != nil {
		in.Pace = string(*b.Pace)
	}
	if b.Telephony != nil {
		in.Telephony = &transcriptions.Telephony{}
		if b.Telephony.Codec != nil {
			in.Telephony.Codec = string(*b.Telephony.Codec)
		}
		if b.Telephony.SampleRate != nil {
			in.Telephony.SampleRate = int(*b.Telephony.SampleRate)
		}
	}
	for _, t := range b.Targets {
		in.Targets = append(in.Targets, transcriptions.TargetIn{CheckpointID: deref(t.CheckpointId), ModelVersionID: deref(t.ModelVersionId),
			BaseModelVersionID: deref(t.BaseModelVersionId), DeploymentID: deref(t.DeploymentId), Profile: deref(t.Profile), Language: deref(t.Language), Boost: deref(t.Boost),
			BoostWeight: t.BoostWeight})
	}
	// Manual tests spend the project's manual-test allowance, not its GPU budget: nothing for the policy to weigh.
	ctx = spending(ctx, 0)
	cmd := command(ctx, "transcriptions.new", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		pl, err := s.transcriptions.Prepare(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: pl.Session}, nil, nil
		}
		ses, drafts, err := s.transcriptions.Open(ctx, tx, pl)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: ses}, drafts, nil
	})
}

// StreamConnect is served by mediaRoutes (a WebSocket upgrade); the strict layer never routes it.
func (s *Server) StreamConnect(context.Context, api.StreamConnectRequestObject) (api.StreamConnectResponseObject, error) {
	return nil, problems.NotImplemented.New("stream.connect is served at the non-strict layer")
}

// WorkerLiveConnect is served by mediaRoutes (a WebSocket upgrade); the strict layer never routes it.
func (s *Server) WorkerLiveConnect(context.Context, api.WorkerLiveConnectRequestObject) (api.WorkerLiveConnectResponseObject, error) {
	return nil, problems.NotImplemented.New("workerLive.connect is served at the non-strict layer")
}

// StreamConnect serves stream.connect: the browser's live socket.
func (m mediaRoutes) StreamConnect(w http.ResponseWriter, r *http.Request, id string, params api.StreamConnectParams) {
	if err := m.s.streamConnect(w, r, id, params.Ticket); err != nil {
		m.s.writeProblem(w, r, err)
	}
}

func (s *Server) streamConnect(w http.ResponseWriter, r *http.Request, id, ticket string) error {
	p, err := mediaViewer(r.Context())
	if err != nil {
		return err
	}
	if !s.transcriptions.OriginAllowed(r) {
		return problems.Forbidden.New("the live socket opens only from this server's own pages (Origin %q); a front end on another host needs CADENCE_ALLOWED_ORIGINS",
			r.Header.Get("Origin"))
	}
	if p.Actor.Kind != auth.KindUser || p.Actor.ID == "" {
		return problems.Forbidden.New("a transcription is a manual test: a person opens its socket")
	}
	return s.transcriptions.ServeClient(w, r, id, ticket, p.Actor.ID)
}

// WorkerLiveConnect serves workerLive.connect: a worker's live job dials its session.
func (m mediaRoutes) WorkerLiveConnect(w http.ResponseWriter, r *http.Request, jobID string, params api.WorkerLiveConnectParams) {
	if err := m.s.workerLiveConnect(w, r, jobID, deref(params.CadenceLiveToken)); err != nil {
		m.s.writeProblem(w, r, err)
	}
}

func (s *Server) workerLiveConnect(w http.ResponseWriter, r *http.Request, jobID, token string) error {
	ctx := r.Context()
	if token != "" {
		if err := s.transcriptions.CheckLiveToken(ctx, jobID, token); err != nil {
			return err
		}
		return s.transcriptions.ServeWorker(w, r, jobID)
	}
	c, err := s.workerCaller(ctx)
	if err != nil {
		return err
	}
	var host string
	err = s.Pool.QueryRow(ctx, `SELECT h.name FROM leases l JOIN compute_hosts h ON h.id = l.host_id
		WHERE l.job_id = $1 AND l.state = 'active'`, jobID).Scan(&host)
	if errors.Is(err, pgx.ErrNoRows) {
		return problems.LeaseEnded.New("job %s has no active lease; claim it before dialling its session", jobID)
	}
	if err != nil {
		return err
	}
	// A worker token dials only the jobs its own host holds the lease of (a token bound to no host dials none); the
	// development actor without a credential is not a token.
	if (c.CredentialID != "" && c.Host == "") || (c.Host != "" && c.Host != host) {
		return problems.Forbidden.New("job %s's lease is held on host %q; this worker token belongs to %q", jobID, host, c.Host)
	}
	return s.transcriptions.ServeWorker(w, r, jobID)
}

// liveTokenRequest reports a worker's live dial that carries the lease's live token instead of a credential: it may
// proceed without a principal and is checked by the handler (publicRequest).
func liveTokenRequest(r *http.Request) bool {
	return r.Method == http.MethodGet && r.Header.Get(transcriptions.HeaderLiveToken) != "" && r.Header.Get("Authorization") == "" &&
		strings.HasPrefix(strings.TrimPrefix(r.URL.Path, APIPrefix), "/worker-live/")
}
