// Package serving is staging serving (docs/spec/06-platform.md "Staging serving", R30; phase 5 · stream D2): the
// control plane's side of the server Cadence reaches. It
//
//   - checks every active staging target's server on a schedule (serving.health_check_seconds) and keeps its health
//     (serving_health; deployment_target.health on entity.deployment_target.{id} when the state changes);
//   - gives a lease that serves a model (a step that consumes a deployable, steps.ServedOf) the target's endpoint and
//     the versioned model name in its environment, and counts the leases per served model (serving_leases,
//     serving_models): the queue reserves a served model's memory once per card, and a model no lease has used for
//     serving.unload_idle_minutes is unloaded;
//   - refuses work for a target that is down (serving-unavailable) or does not serve (target-does-not-serve).
//
// It never names a server's protocol: each server kind's health, index and unload paths are data
// (defaults.yaml serving.servers). The serve steps themselves load models and speak the stream protocol; they are
// the family pack's. Nothing here reaches a delivery target: those have no endpoint (non-negotiable 8).
package serving

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

// The lease environment of a served step (the worker's serve kind reads it; none is a secret, but the grant hook's
// variables travel with the lease only, never into a log or an event).
const (
	EnvTarget        = "CADENCE_SERVING_TARGET"         // dtg_…
	EnvEndpoint      = "CADENCE_SERVING_ENDPOINT"       // the staging target's endpoint
	EnvServer        = "CADENCE_SERVING_SERVER"         // the server kind
	EnvServerVersion = "CADENCE_SERVING_SERVER_VERSION" // the server version
	EnvModel         = "CADENCE_SERVING_MODEL"          // the versioned model name to load and stream to (the first)
	EnvModels        = "CADENCE_SERVING_MODELS"         // JSON: deployable input name → its model name
	// EnvRefused says why the lease may not serve (the step fails with it): an unknown, archived or delivery target,
	// or one that does not serve the deployable's family, format or profile.
	EnvRefused = "CADENCE_SERVING_REFUSED"
)

// Health states.
const (
	StateUnknown = "unknown"
	StateUp      = "up"
	StateDown    = "down"
)

// Served model states (the contract's ServedModel.state; in-use is derived from the lease count).
const (
	ModelInUse    = "in-use"
	ModelLoaded   = "loaded"
	ModelUnloaded = "unloaded"
)

// EventHealth is emitted on the target's topic when its server's state changes.
const EventHealth = "deployment_target.health"

// Health is a staging target's server as the last check found it (the contract's ServingHealth).
type Health struct {
	State     string     `json:"state"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	Since     *time.Time `json:"since,omitempty"`
	Detail    string     `json:"detail,omitempty"`
	LatencyMs *int       `json:"latencyMs,omitempty"`
}

// Model is a served model with its lease count (the contract's ServedModel).
type Model struct {
	Model          string     `json:"model"`
	DeployableHash string     `json:"deployableHash"`
	MemoryMB       int        `json:"memoryMb"`
	State          string     `json:"state"`
	Leases         int        `json:"leases"`
	LastUsedAt     *time.Time `json:"lastUsedAt,omitempty"`
	UnloadedAt     *time.Time `json:"unloadedAt,omitempty"`

	targetID string
	idleFor  time.Duration
}

// Service is staging serving on one database.
type Service struct {
	Pool     *pgxpool.Pool
	Client   *http.Client // nil: a client with serving.health_timeout_s
	Defaults func() *defaults.Defaults
	Log      *slog.Logger
	Now      func() time.Time
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (s *Service) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: time.Duration(s.defaults().Serving.HealthTimeoutSeconds.Value) * time.Second}
}

// FallbackMB is a served model's reservation when neither its deployable nor its step states one
// (serving.model_memory_gb).
func FallbackMB(d *defaults.Defaults) int { return int(d.Serving.ModelMemoryGB.Value * 1024) }

// ---------------------------------------------------------------- leases

// Granted is the worker protocol's grant hook (workers.Service.OnGranted): a lease that serves a model gets the
// staging target's endpoint, server and the model's name in its environment, and the lease is counted against the
// model. A target the lease may not serve through is named in CADENCE_SERVING_REFUSED instead, and the step fails
// with it; the grant itself never fails for it (the pipeline checked the target when it was planned).
func (s *Service) Granted(ctx context.Context, tx pgx.Tx, leaseID, _ string, spec steps.Spec) (map[string]string, error) {
	d := s.defaults()
	all := steps.ServedModels(spec, FallbackMB(d))
	if len(all) == 0 {
		return nil, nil
	}
	ref := all[0].Target
	if ref == "" {
		ref = d.Serving.DefaultTarget.Value
	}
	byInput := make(map[string]string, len(all))
	for _, sv := range all {
		byInput[sv.Input] = sv.Model
	}
	for name, in := range spec.Inputs { // inputs that repeat a deployable share its model
		if in.Type == steps.TypeDeployable {
			byInput[name] = steps.ServedModelName(in.Hash)
		}
	}
	models, err := json.Marshal(byInput)
	if err != nil {
		return nil, fmt.Errorf("encode the served models: %w", err)
	}
	env := map[string]string{EnvModel: all[0].Model, EnvModels: string(models)}
	t, err := targets.Get(ctx, tx, ref)
	if err != nil {
		if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
			env[EnvRefused] = fmt.Sprintf("%s: no deployment target %q (deploymentTargets.list names them)", problems.TargetDoesNotServe.Slug, ref)
			return env, nil
		}
		return nil, err
	}
	if why := refusal(t, deployableOf(spec)); why != "" {
		env[EnvRefused] = problems.TargetDoesNotServe.Slug + ": " + why
		return env, nil
	}
	env[EnvTarget], env[EnvEndpoint] = t.ID, t.Endpoint
	env[EnvServer], env[EnvServerVersion] = t.Server.Kind, t.Server.Version
	now := s.now()
	for _, sv := range all {
		if _, err := tx.Exec(ctx, `INSERT INTO serving_leases (lease_id, target_id, model, memory_mb, created_at)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, leaseID, t.ID, sv.Model, sv.MemoryMB, now); err != nil {
			return nil, fmt.Errorf("count the served model's lease: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO serving_models (target_id, model, deployable_hash, memory_mb, state, loaded_at, last_used_at)
			VALUES ($1, $2, $3, $4, 'loaded', $5, $5)
			ON CONFLICT (target_id, model) DO UPDATE SET state = 'loaded', last_used_at = excluded.last_used_at,
				memory_mb = excluded.memory_mb, unloaded_at = NULL,
				loaded_at = CASE WHEN serving_models.state = 'unloaded' THEN excluded.loaded_at ELSE serving_models.loaded_at END`,
			t.ID, sv.Model, sv.DeployableHash, sv.MemoryMB, now); err != nil {
			return nil, fmt.Errorf("record the served model: %w", err)
		}
	}
	return env, nil
}

// Deployable is what a deployable's metadata says about what it is (cadence.deployable/1's family, format and
// profile), when the producing step published them.
type Deployable struct {
	Family  string `json:"family"`
	Format  string `json:"format"`
	Profile string `json:"profile"`
}

func deployableOf(spec steps.Spec) Deployable {
	var out Deployable
	for _, in := range spec.Inputs {
		if in.Type == steps.TypeDeployable && len(in.Meta) > 0 {
			_ = json.Unmarshal(in.Meta, &out)
			return out
		}
	}
	return out
}

// refusal is why t may not serve dep ("" when it may): it must be an active staging target that serves the
// deployable's family, format and profile (those the deployable names; R46).
func refusal(t targets.Target, dep Deployable) string {
	switch {
	case t.State != targets.StateActive:
		return fmt.Sprintf("deployment target %s is archived", t.Name)
	case t.Kind != targets.KindStaging:
		return fmt.Sprintf("deployment target %s is a delivery target: Cadence never reaches it (only its delivery script does); serve through a staging target", t.Name)
	case t.Endpoint == "":
		return fmt.Sprintf("staging target %s names no endpoint", t.Name)
	}
	if dep.Family == "" {
		return ""
	}
	for _, sv := range t.Serves {
		if sv.Family != dep.Family {
			continue
		}
		if dep.Format != "" && !contains(sv.Formats, dep.Format) {
			return fmt.Sprintf("target %s serves %s in %s, not %s", t.Name, dep.Family, strings.Join(sv.Formats, ", "), dep.Format)
		}
		if dep.Profile != "" && !contains(sv.Profiles, dep.Profile) {
			return fmt.Sprintf("target %s serves %s at %s, not at %s", t.Name, dep.Family, strings.Join(sv.Profiles, ", "), dep.Profile)
		}
		return ""
	}
	return fmt.Sprintf("target %s does not serve model family %s (it serves %s)", t.Name, dep.Family, servesList(t))
}

func servesList(t targets.Target) string {
	if len(t.Serves) == 0 {
		return "nothing yet: deploymentTargets.edit adds what it serves"
	}
	names := make([]string, 0, len(t.Serves))
	for _, sv := range t.Serves {
		names = append(names, sv.Family)
	}
	return strings.Join(names, ", ")
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Check is the request-time check of work that will serve dep through target ref (default serving.default_target):
// target-does-not-serve when it may not, serving-unavailable when the last health check found its server down. A
// server not checked yet passes; its step fails serving-unavailable (retryable) if it is down.
func (s *Service) Check(ctx context.Context, q storage.Querier, ref string, dep Deployable) (targets.Target, error) {
	if strings.TrimSpace(ref) == "" {
		ref = s.defaults().Serving.DefaultTarget.Value
	}
	t, err := targets.Get(ctx, q, ref)
	if err != nil {
		if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
			return targets.Target{}, problems.TargetDoesNotServe.New("no deployment target %q (deploymentTargets.list names them)", ref)
		}
		return targets.Target{}, err
	}
	if why := refusal(t, dep); why != "" {
		return targets.Target{}, problems.TargetDoesNotServe.New("%s", why)
	}
	hs, err := HealthOf(ctx, q, []string{t.ID})
	if err != nil {
		return targets.Target{}, err
	}
	if h := hs[t.ID]; h.State == StateDown {
		return targets.Target{}, problems.ServingUnavailable.New("the server of staging target %s is down (%s, since %s); start it with docker compose --profile serving up -d",
			t.Name, h.Detail, h.Since.UTC().Format(time.RFC3339))
	}
	return t, nil
}

// ---------------------------------------------------------------- reading

// HealthOf reads the health of targets ids (unknown for a target never checked).
func HealthOf(ctx context.Context, q storage.Querier, ids []string) (map[string]Health, error) {
	out := make(map[string]Health, len(ids))
	for _, id := range ids {
		out[id] = Health{State: StateUnknown}
	}
	rows, err := q.Query(ctx, `SELECT target_id, state, detail, latency_ms, checked_at, since FROM serving_health
		WHERE target_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read serving health: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id         string
			h          Health
			lat        *int
			chk, since time.Time
		)
		if err := rows.Scan(&id, &h.State, &h.Detail, &lat, &chk, &since); err != nil {
			return nil, fmt.Errorf("read serving health: %w", err)
		}
		h.LatencyMs, h.CheckedAt, h.Since = lat, &chk, &since
		out[id] = h
	}
	return out, rows.Err()
}

// Models reads the served models of targets ids with their active lease counts, by target then model name.
func (s *Service) Models(ctx context.Context, q storage.Querier, ids []string) (map[string][]Model, error) {
	return models(ctx, q, ids, s.now())
}

func models(ctx context.Context, q storage.Querier, ids []string, now time.Time) (map[string][]Model, error) {
	rows, err := q.Query(ctx, `SELECT m.target_id, m.model, m.deployable_hash, m.memory_mb, m.state, m.last_used_at, m.unloaded_at,
			count(l.id) FILTER (WHERE l.state = 'active'),
			greatest(m.last_used_at, max(l.ended_at))
		FROM serving_models m
		LEFT JOIN serving_leases sl ON sl.target_id = m.target_id AND sl.model = m.model
		LEFT JOIN leases l ON l.id = sl.lease_id
		WHERE m.target_id = ANY($1)
		GROUP BY m.target_id, m.model ORDER BY m.target_id, m.model`, ids)
	if err != nil {
		return nil, fmt.Errorf("read served models: %w", err)
	}
	defer rows.Close()
	out := map[string][]Model{}
	for rows.Next() {
		var (
			m        Model
			lastUsed time.Time
			idle     time.Time
		)
		if err := rows.Scan(&m.targetID, &m.Model, &m.DeployableHash, &m.MemoryMB, &m.State, &lastUsed, &m.UnloadedAt, &m.Leases, &idle); err != nil {
			return nil, fmt.Errorf("read served models: %w", err)
		}
		m.LastUsedAt = &idle
		m.idleFor = now.Sub(idle)
		if m.Leases > 0 {
			m.State, m.idleFor = ModelInUse, 0
		}
		out[m.targetID] = append(out[m.targetID], m)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- the periodic check

// Tick checks every active staging target's server, records its health, and on a server that is up reconciles the
// served models with its index and unloads those no lease has used for serving.unload_idle_minutes. One target's
// failure does not stop the others.
func (s *Service) Tick(ctx context.Context) error {
	list, err := targets.List(ctx, s.Pool, false)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range list {
		if t.Kind != targets.KindStaging || t.Endpoint == "" {
			continue
		}
		paths, known := s.defaults().Serving.Servers.Value[t.Server.Kind]
		if !known || paths.Health == "" {
			continue // a server kind defaults.yaml gives no paths for is never checked
		}
		h := s.probe(ctx, t, paths)
		if err := s.record(ctx, t, h); err != nil {
			errs = append(errs, err)
			continue
		}
		if h.State != StateUp {
			continue
		}
		if err := s.reconcile(ctx, t, paths); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// call makes one request to the target's server; the body is read and closed.
func (s *Service) call(ctx context.Context, t targets.Target, method, path string, body []byte) (int, []byte, error) {
	u, err := url.JoinPath(t.Endpoint, path)
	if err != nil {
		return 0, nil, err
	}
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

func (s *Service) probe(ctx context.Context, t targets.Target, paths defaults.ServerPaths) Health {
	now := s.now()
	start := time.Now()
	code, _, err := s.call(ctx, t, http.MethodGet, paths.Health, nil)
	lat := int(time.Since(start) / time.Millisecond)
	h := Health{State: StateUp, CheckedAt: &now, LatencyMs: &lat}
	switch {
	case err != nil:
		h.State, h.Detail = StateDown, truncate(err.Error(), 500)
	case code < 200 || code > 299:
		h.State, h.Detail = StateDown, fmt.Sprintf("%s answered %d", paths.Health, code)
	}
	return h
}

// record stores h for t and emits the change when the state changed.
func (s *Service) record(ctx context.Context, t targets.Target, h Health) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var prev string
		err := tx.QueryRow(ctx, "SELECT state FROM serving_health WHERE target_id = $1 FOR UPDATE", t.ID).Scan(&prev)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read serving health: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO serving_health (target_id, state, detail, latency_ms, checked_at, since)
			VALUES ($1, $2, $3, $4, $5, $5)
			ON CONFLICT (target_id) DO UPDATE SET state = excluded.state, detail = excluded.detail,
				latency_ms = excluded.latency_ms, checked_at = excluded.checked_at,
				since = CASE WHEN serving_health.state = excluded.state THEN serving_health.since ELSE excluded.since END`,
			t.ID, h.State, h.Detail, h.LatencyMs, *h.CheckedAt); err != nil {
			return fmt.Errorf("record serving health: %w", err)
		}
		if prev == h.State {
			return nil
		}
		if h.State == StateDown {
			s.log().WarnContext(ctx, "staging server down", "target", t.Name, "detail", h.Detail)
		} else {
			s.log().InfoContext(ctx, "staging server up", "target", t.Name)
		}
		return events.Append(ctx, tx, jobs.System, nil, []events.Draft{{Topic: targets.Topic(t.ID), Type: EventHealth,
			Payload: map[string]any{"id": t.ID, "name": t.Name, "state": h.State, "detail": h.Detail, "checkedAt": h.CheckedAt}}})
	})
}

// indexEntry is one model of a server's repository index (the KServe v2 repository extension's shape).
type indexEntry struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// reconcile marks models the server no longer has loaded and no lease uses as unloaded, then unloads idle models.
func (s *Service) reconcile(ctx context.Context, t targets.Target, paths defaults.ServerPaths) error {
	ms, err := models(ctx, s.Pool, []string{t.ID}, s.now())
	if err != nil {
		return err
	}
	ready := map[string]bool{}
	indexed := false
	if paths.Index != "" {
		code, body, err := s.call(ctx, t, http.MethodPost, paths.Index, []byte(`{}`))
		var list []indexEntry
		if err == nil && code >= 200 && code <= 299 && json.Unmarshal(body, &list) == nil {
			indexed = true
			for _, e := range list {
				if strings.EqualFold(e.State, "READY") {
					ready[e.Name] = true
				}
			}
		}
	}
	idle := time.Duration(s.defaults().Serving.UnloadIdleMinutes.Value) * time.Minute
	var errs []error
	for _, m := range ms[t.ID] {
		if m.State != ModelLoaded {
			continue // in use, or unloaded already
		}
		switch {
		case indexed && !ready[m.Model]:
			errs = append(errs, s.markUnloaded(ctx, t, m, "the server no longer has it loaded"))
		case m.idleFor >= idle && paths.Unload != "":
			code, body, err := s.call(ctx, t, http.MethodPost, strings.ReplaceAll(paths.Unload, "{model}", url.PathEscape(m.Model)), []byte(`{}`))
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("unload %s from %s: %w", m.Model, t.Name, err))
			case code >= 200 && code <= 299:
				errs = append(errs, s.markUnloaded(ctx, t, m, fmt.Sprintf("no lease used it for %s", idle)))
			default:
				errs = append(errs, fmt.Errorf("unload %s from %s: %d %s", m.Model, t.Name, code, truncate(string(body), 200)))
			}
		}
	}
	return errors.Join(errs...)
}

// markUnloaded sets m unloaded unless a lease started using it meanwhile.
func (s *Service) markUnloaded(ctx context.Context, t targets.Target, m Model, why string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE serving_models SET state = 'unloaded', unloaded_at = $3
		WHERE target_id = $1 AND model = $2 AND state = 'loaded' AND NOT EXISTS (
			SELECT 1 FROM serving_leases sl JOIN leases l ON l.id = sl.lease_id
			WHERE sl.target_id = $1 AND sl.model = $2 AND l.state = 'active')`, t.ID, m.Model, s.now())
	if err != nil {
		return fmt.Errorf("mark %s unloaded: %w", m.Model, err)
	}
	if tag.RowsAffected() > 0 {
		s.log().InfoContext(ctx, "served model unloaded", "target", t.Name, "model", m.Model, "why", why)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
