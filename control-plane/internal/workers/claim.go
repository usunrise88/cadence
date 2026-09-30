package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/queue"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// CardTelemetry is one card as a worker sees it (the contract's CardTelemetry).
type CardTelemetry struct {
	Index         int      `json:"index"`
	Name          string   `json:"name,omitempty"`
	MemoryTotalMB *int     `json:"memoryTotalMb,omitempty"`
	MemoryUsedMB  *int     `json:"memoryUsedMb,omitempty"`
	Utilization   *float64 `json:"utilization,omitempty"`
	TemperatureC  *float64 `json:"temperatureC,omitempty"`
	PowerW        *float64 `json:"powerW,omitempty"`
}

func (c CardTelemetry) freeMB() *int {
	if c.MemoryTotalMB == nil || c.MemoryUsedMB == nil {
		return nil
	}
	f := *c.MemoryTotalMB - *c.MemoryUsedMB
	return &f
}

// Claim is a worker's long-poll (the contract's WorkerClaim).
type Claim struct {
	WorkerID string          `json:"workerId"`
	Wait     *int            `json:"wait,omitempty"`
	Cards    []CardTelemetry `json:"cards"`
}

// GrantCard is the card of a lease; Index is -1 for a step that needs no card.
type GrantCard struct {
	Index       int `json:"index"`
	MemoryCapMB int `json:"memoryCapMb"`
}

// Grant is a lease as the worker receives it (the contract's Lease).
type Grant struct {
	ID               string            `json:"id"`
	JobID            string            `json:"jobId"`
	Spec             steps.Spec        `json:"spec"`
	Inputs           map[string]string `json:"inputs"`
	Card             GrantCard         `json:"card"`
	Env              map[string]string `json:"env,omitempty"` // secret values: never log a Grant
	Traceparent      string            `json:"traceparent"`
	HeartbeatSeconds int               `json:"heartbeatSeconds"`
}

// Claim long-polls for a step the worker may run: it answers a lease as soon as one fits, or nil when the wait
// passes (at most 30 s). Every attempt records the worker as seen and its card telemetry.
func (s *Service) Claim(ctx context.Context, c Caller, in Claim) (*Grant, error) {
	wait := 20 * time.Second
	if in.Wait != nil {
		wait = min(time.Duration(*in.Wait)*time.Second, MaxClaimWait)
	}
	deadline := time.Now().Add(wait)
	first := true
	for {
		var g *Grant
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var (
				drafts []events.Draft
				err    error
			)
			g, drafts, err = s.claimOnce(ctx, tx, c, in, first)
			if err != nil {
				return err
			}
			return events.Append(ctx, tx, jobs.System, nil, drafts)
		})
		first = false
		if err != nil || g != nil {
			if g != nil {
				s.log.InfoContext(ctx, "lease granted", "lease", g.ID, "job", g.JobID, "kind", g.Spec.KindRef(), "card", g.Card.Index)
				s.wake.broadcast()
			}
			return g, err
		}
		left := time.Until(deadline)
		if left <= 0 || !s.sleep(ctx, left) {
			return nil, nil
		}
	}
}

type candidate struct {
	jobID string
	spec  steps.Spec
	trace string // the job span's traceparent, "" when it was queued without one
}

func (s *Service) claimOnce(ctx context.Context, tx pgx.Tx, c Caller, in Claim, telemetry bool) (*Grant, []events.Draft, error) {
	w, err := s.worker(ctx, tx, c, in.WorkerID)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if _, err := tx.Exec(ctx, "UPDATE workers SET last_seen_at = $2 WHERE id = $1", w.ID, now); err != nil {
		return nil, nil, fmt.Errorf("touch worker: %w", err)
	}
	var drafts []events.Draft
	if telemetry {
		if drafts, err = s.recordTelemetry(ctx, tx, w, in.Cards, now); err != nil {
			return nil, nil, err
		}
	}
	cands, err := waiting(ctx, tx, w.StepKinds)
	if err != nil || len(cands) == 0 {
		return nil, drafts, err
	}
	host, err := compute.Get(ctx, tx, w.HostID)
	if err != nil {
		return nil, nil, err
	}
	var cards []queue.Card
	if slices.ContainsFunc(cands, func(c candidate) bool { return c.spec.Resources.GPU }) {
		tz, err := instanceZone(ctx, tx)
		if err != nil {
			return nil, nil, err
		}
		if cards, err = lockCards(ctx, tx, host, in.Cards, tz); err != nil {
			return nil, nil, err
		}
	}
	for _, cand := range cands {
		card, capMB, ok := place(cand, cards, now)
		if !ok {
			continue
		}
		env, missing := s.secretEnv(ctx, cand.spec.SecretNames)
		if missing != "" {
			d, err := s.endWaiting(ctx, tx, cand.jobID, steps.Outcome{State: steps.StateFailed,
				Error: &steps.StepError{Type: steps.ErrInput, Message: missing}})
			if err != nil {
				return nil, nil, err
			}
			drafts = append(drafts, d...)
			continue
		}
		g, d, err := s.lease(ctx, tx, w, host, cand, card, capMB, now)
		if err != nil {
			return nil, nil, err
		}
		g.Env = env
		return g, append(drafts, d...), nil
	}
	return nil, drafts, nil
}

// place picks the card for cand: the first by index where it fits, or no card for a step without a GPU.
func place(cand candidate, cards []queue.Card, now time.Time) (*queue.Card, int, bool) {
	if !cand.spec.Resources.GPU {
		return nil, 0, true
	}
	need := queue.NeedOf(cand.spec)
	for i := range cards {
		if capMB, why := queue.Fit(cards[i], need, now); why == "" {
			return &cards[i], capMB, true
		}
	}
	return nil, 0, false
}

// projectPriority is the SQL expression of a step job's project queue priority (projects p joined on s.project_id):
// the project's budgets.queuePriority, or $n — defaults.yaml budgets.queue_priority_per_project — for a project that
// never set one and for a job without a project. It is read live, so a projects.edit reorders waiting jobs at once.
func projectPriority(param string) string {
	return `coalesce((p.budgets->>'queuePriority')::int, ` + param + `)`
}

// defaultProjectPriority is defaults.yaml budgets.queue_priority_per_project.
func defaultProjectPriority() int { return defaults.Get().Budgets.QueuePriorityPerProject.Value }

// waiting reads the step jobs a worker with these kinds may run, in start order: the project's queue priority, then
// the job's priority, then first come (spec 02 "Budgets": the queue interleaves projects by priority). Rows are
// locked, skipping those another claim holds.
func waiting(ctx context.Context, tx pgx.Tx, kinds []string) ([]candidate, error) {
	rows, err := tx.Query(ctx, `SELECT s.job_id, s.spec, coalesce(s.traceparent, '') FROM step_jobs s JOIN jobs j ON j.id = s.job_id
		LEFT JOIN projects p ON p.id = s.project_id
		WHERE s.state = 'waiting' AND s.kind_ref = ANY($1) AND j.paused_at IS NULL AND j.cancel_requested_at IS NULL
		ORDER BY `+projectPriority("$2")+` DESC, j.priority DESC, s.enqueued_at, s.job_id
		LIMIT 50 FOR UPDATE OF s SKIP LOCKED`, kinds, defaultProjectPriority())
	if err != nil {
		return nil, fmt.Errorf("read the step queue: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (candidate, error) {
		var (
			c   candidate
			raw []byte
		)
		if err := row.Scan(&c.jobID, &raw, &c.trace); err != nil {
			return c, err
		}
		return c, json.Unmarshal(raw, &c.spec)
	})
}

// instanceZone is the instance time zone (policies.timezone) that availability windows naming none follow.
func instanceZone(ctx context.Context, q storage.Querier) (string, error) {
	p, err := policies.Get(ctx, q, defaults.Get())
	if err != nil {
		return "", err
	}
	return p.Timezone, nil
}

// lockCards takes the card slot locks of the host's cards the worker reported, in index order (so two claims never
// deadlock), and reads what each card holds after the lock. Windows without a time zone take tz.
func lockCards(ctx context.Context, tx pgx.Tx, host compute.Host, reported []CardTelemetry, tz string) ([]queue.Card, error) {
	var out []queue.Card
	cfg := slices.Clone(host.Cards)
	slices.SortFunc(cfg, func(a, b compute.Card) int { return a.Index - b.Index })
	for _, cc := range cfg {
		at := slices.IndexFunc(reported, func(r CardTelemetry) bool { return r.Index == cc.Index })
		if at < 0 {
			continue // this worker cannot see the card
		}
		if _, err := tx.Exec(ctx, `INSERT INTO card_slots (host_id, card_index) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			host.ID, cc.Index); err != nil {
			return nil, fmt.Errorf("card slot: %w", err)
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM card_slots WHERE host_id = $1 AND card_index = $2 FOR UPDATE`,
			host.ID, cc.Index); err != nil {
			return nil, fmt.Errorf("lock card slot: %w", err)
		}
		cc.Windows = cc.Windows.InZone(tz)
		out = append(out, queue.Card{Config: cc, FreeMB: reported[at].freeMB()})
	}
	if len(out) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT card_index, job_kind, memory_mb FROM leases
		WHERE host_id = $1 AND state = 'active' AND card_index IS NOT NULL`, host.ID)
	if err != nil {
		return nil, fmt.Errorf("read card leases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			idx int
			h   queue.Held
		)
		if err := rows.Scan(&idx, &h.JobKind, &h.MemoryMB); err != nil {
			return nil, fmt.Errorf("read card leases: %w", err)
		}
		for i := range out {
			if out[i].Config.Index == idx {
				out[i].Held = append(out[i].Held, h)
			}
		}
	}
	return out, rows.Err()
}

// secretEnv reads the secrets a step declares; missing names the first one that cannot be read. Values go into the
// lease answer only: never into the lease row, a log line or an event.
func (s *Service) secretEnv(ctx context.Context, names []string) (map[string]string, string) {
	if len(names) == 0 {
		return nil, ""
	}
	env := make(map[string]string, len(names))
	for _, n := range names {
		if s.secrets == nil {
			return nil, fmt.Sprintf("the step needs secret %q and this control plane has no secret store", n)
		}
		v, err := s.secrets.Read(ctx, n)
		if err != nil {
			return nil, fmt.Sprintf("the step needs secret %q: %v", n, err)
		}
		env[envName(n)] = string(v)
	}
	return env, ""
}

// envName is the environment variable of secret name: upper case, dashes as underscores (hf-token → HF_TOKEN).
func envName(name string) string {
	b := []byte(name)
	for i, ch := range b {
		switch {
		case ch >= 'a' && ch <= 'z':
			b[i] = ch - 'a' + 'A'
		case ch == '-' || ch == '.':
			b[i] = '_'
		}
	}
	return string(b)
}

// lease records the lease of cand on card (nil: no card) and moves the job out of the waiting queue.
func (s *Service) lease(ctx context.Context, tx pgx.Tx, w Worker, host compute.Host, cand candidate, card *queue.Card, capMB int,
	now time.Time) (*Grant, []events.Draft, error) {
	id := "lse_" + uuid.Must(uuid.NewV7()).String()
	jobKind := queue.NeedOf(cand.spec).JobKind
	var cardIndex *int
	gc := GrantCard{Index: -1}
	if card != nil {
		idx := card.Config.Index
		cardIndex, gc = &idx, GrantCard{Index: idx, MemoryCapMB: capMB}
		card.Held = append(card.Held, queue.Held{JobKind: jobKind, MemoryMB: capMB})
	}
	if _, err := tx.Exec(ctx, `INSERT INTO leases (id, job_id, worker_id, host_id, card_index, job_kind, memory_mb, created_at, heartbeat_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`, id, cand.jobID, w.ID, host.ID, cardIndex, jobKind, capMB, now); err != nil {
		return nil, nil, fmt.Errorf("record lease: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE step_jobs SET state = 'leased', updated_at = $2 WHERE job_id = $1`, cand.jobID, now); err != nil {
		return nil, nil, fmt.Errorf("lease step job: %w", err)
	}
	where := host.Name
	if card != nil {
		where = fmt.Sprintf("%s card %d", host.Name, card.Config.Index)
	}
	drafts, err := jobs.Progress(ctx, tx, cand.jobID, 0, "running on "+where)
	if err != nil {
		return nil, nil, err
	}
	if s.onLeased != nil {
		more, err := s.onLeased(ctx, tx, cand.jobID)
		if err != nil {
			return nil, nil, fmt.Errorf("lease step job: %w", err)
		}
		drafts = append(drafts, more...)
	}
	drafts = append(drafts, queueEvent(cand.jobID, cand.spec.ProjectID, "leased", map[string]any{
		"leaseId": id, "workerId": w.ID, "host": host.Name, "card": gc.Index}))
	inputs := make(map[string]string, len(cand.spec.Inputs))
	for name, ref := range cand.spec.Inputs {
		inputs[name] = "cas://" + ref.Hash
	}
	return &Grant{ID: id, JobID: cand.jobID, Spec: cand.spec, Inputs: inputs, Card: gc,
		Traceparent: leaseTrace(cand, id), HeartbeatSeconds: s.beat}, drafts, nil
}

func queueEvent(jobID, projectID, change string, extra map[string]any) events.Draft {
	p := map[string]any{"jobId": jobID, "change": change}
	for k, v := range extra {
		p[k] = v
	}
	return events.Draft{Topic: TopicQueue, Type: EventQueue, ProjectID: projectID, Payload: p}
}

// recordTelemetry keeps each reported card's telemetry in its slot; at most every GPUEventInterval per host it also
// emits it on the gpu topic and marks the host healthy.
func (s *Service) recordTelemetry(ctx context.Context, tx pgx.Tx, w Worker, cards []CardTelemetry, now time.Time) ([]events.Draft, error) {
	for _, c := range cards {
		if _, err := tx.Exec(ctx, `INSERT INTO card_slots (host_id, card_index, telemetry, reported_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (host_id, card_index) DO UPDATE SET telemetry = excluded.telemetry, reported_at = excluded.reported_at`,
			w.HostID, c.Index, c, now); err != nil {
			return nil, fmt.Errorf("record card telemetry: %w", err)
		}
	}
	var sent string
	err := tx.QueryRow(ctx, `INSERT INTO gpu_events (host_id, sent_at) VALUES ($1, $2)
		ON CONFLICT (host_id) DO UPDATE SET sent_at = excluded.sent_at WHERE gpu_events.sent_at <= $3 RETURNING host_id`,
		w.HostID, now, now.Add(-GPUEventInterval)).Scan(&sent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // throttled
	}
	if err != nil {
		return nil, fmt.Errorf("throttle gpu events: %w", err)
	}
	drafts, err := compute.SetHealth(ctx, tx, w.HostID, compute.Health{State: "healthy", CheckedAt: &now})
	if err != nil {
		return nil, err
	}
	if cards == nil {
		cards = []CardTelemetry{}
	}
	return append(drafts, events.Draft{Topic: TopicGPU, Type: EventGPU,
		Payload: map[string]any{"hostId": w.HostID, "host": w.Host, "cards": cards, "at": now}}), nil
}
