// Package agentcreds is the agents' own model accounts, configured instance-wide by the admin (Settings → Agents):
// the Claude Code subscription token and opencode's providers (docs/spec/08-resolutions.md R3, R6).
//
// Values never enter Postgres, a log line, an event, an audit row or a response. A submitted value is sealed into
// the secret store's transit area under the id of a `write` task; the agent host claims the task through the host
// protocol (hostCredentials.claim), writes the value into the agent-credentials volume in the format its agent
// reads, and acknowledges (hostCredentials.report); then the transit copy is deleted. Postgres keeps metadata only:
// agent, provider, a four-character hint, who set it and when, delivery and verification state, the model list and
// opencode's default model. Verification is a `verify` task: the host sends a tiny real request through the agent.
//
// Mutations take a transaction and return the events they emit; reads take any storage.Querier.
package agentcreds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the EntityKind (and topic segment) of an agent credential.
const Kind = "agent_credential"

// HostLapse is how recently an agent host must have claimed work to count as connected.
const HostLapse = 90 * time.Second

// ClaimLease is how long a claimed task waits for its acknowledgement before it is offered again.
const ClaimLease = 2 * time.Minute

// Delivery and verification states.
const (
	DeliveryPending  = "pending"
	DeliveryWritten  = "written"
	DeliveryRemoving = "removing"
	DeliveryRemoved  = "removed"
	DeliveryFailed   = "failed"

	VerifyNone    = "none"
	VerifyPending = "pending"
	VerifyOK      = "ok"
	VerifyFailed  = "failed"
)

// Task actions.
const (
	ActionWrite  = "write"
	ActionRemove = "remove"
	ActionVerify = "verify"
)

// Transit is the part of the secret store that carries values to the agent host (secrets.Store).
type Transit interface {
	SealTransit(name string, value []byte) error
	OpenTransit(name string) ([]byte, error)
	DropTransit(name string) error
}

// State is a delivery or verification state with its detail.
type State struct {
	State  string     `json:"state"`
	Detail string     `json:"detail,omitempty"`
	Model  string     `json:"model,omitempty"`
	At     *time.Time `json:"at,omitempty"`
}

// Credential is one row, without a value (there is none to have). Its JSON form is the contract's AgentCredential.
type Credential struct {
	ID           string      `json:"id"`
	Agent        string      `json:"agent"`
	Provider     string      `json:"provider"`
	CatalogueID  string      `json:"catalogueId"`
	Name         string      `json:"name"`
	BaseURL      string      `json:"baseUrl,omitempty"`
	HasValue     bool        `json:"hasValue"`
	Hint         string      `json:"hint,omitempty"`
	SetAt        *time.Time  `json:"setAt,omitempty"`
	SetBy        *auth.Actor `json:"setBy,omitempty"`
	ExpiresAt    *time.Time  `json:"expiresAt,omitempty"`
	Delivery     State       `json:"delivery"`
	Verification State       `json:"verification"`
	Models       []string    `json:"models"`
	DefaultModel string      `json:"defaultModel,omitempty"`
	Hosts        []string    `json:"hosts"`
	ArchivedAt   *time.Time  `json:"archivedAt,omitempty"`
	Rev          int         `json:"rev"`
	CreatedAt    time.Time   `json:"createdAt"`
	UpdatedAt    time.Time   `json:"updatedAt"`
}

// Live reports whether the credential is configured (not archived).
func (c Credential) Live() bool { return c.ArchivedAt == nil }

const cols = `id, agent, provider, catalogue_id, name, coalesce(base_url, ''), has_value, hint, set_at, set_by, expires_at,
	delivery_state, delivery_detail, delivery_at, verify_state, verify_detail, verify_model, verified_at, models,
	coalesce(default_model, ''), rev, created_at, updated_at, archived_at`

func scan(row pgx.CollectableRow) (Credential, error) {
	var (
		c      Credential
		setBy  []byte
		models []byte
	)
	if err := row.Scan(&c.ID, &c.Agent, &c.Provider, &c.CatalogueID, &c.Name, &c.BaseURL, &c.HasValue, &c.Hint, &c.SetAt,
		&setBy, &c.ExpiresAt, &c.Delivery.State, &c.Delivery.Detail, &c.Delivery.At, &c.Verification.State,
		&c.Verification.Detail, &c.Verification.Model, &c.Verification.At, &models, &c.DefaultModel, &c.Rev,
		&c.CreatedAt, &c.UpdatedAt, &c.ArchivedAt); err != nil {
		return Credential{}, err
	}
	if len(setBy) > 0 && string(setBy) != "null" {
		var a auth.Actor
		if err := json.Unmarshal(setBy, &a); err != nil {
			return Credential{}, fmt.Errorf("decode set_by of %s: %w", c.ID, err)
		}
		c.SetBy = &a
	}
	c.Models = []string{}
	if len(models) > 0 {
		if err := json.Unmarshal(models, &c.Models); err != nil {
			return Credential{}, fmt.Errorf("decode models of %s: %w", c.ID, err)
		}
	}
	c.Hosts = HostsOf(c.CatalogueID, c.BaseURL)
	return c, nil
}

func one(rows pgx.Rows, err error, id string) (Credential, error) {
	if err != nil {
		return Credential{}, fmt.Errorf("query agent credential: %w", err)
	}
	c, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, problems.NotFound.New("no agent credential %q (agentCredentials.list)", id)
	}
	if err != nil {
		return Credential{}, fmt.Errorf("read agent credential %s: %w", id, err)
	}
	return c, nil
}

// Get returns the credential with id, archived or not.
func Get(ctx context.Context, q storage.Querier, id string) (Credential, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM agent_credentials WHERE id = $1", id)
	return one(rows, err, id)
}

// List returns the live credentials and the archived ones whose removal the agent host has not confirmed yet,
// Claude Code first, then opencode's by name.
func List(ctx context.Context, q storage.Querier) ([]Credential, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+` FROM agent_credentials
		WHERE archived_at IS NULL OR delivery_state IN ('removing', 'failed')
		ORDER BY agent, archived_at NULLS FIRST, name, id`)
	if err != nil {
		return nil, fmt.Errorf("list agent credentials: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("list agent credentials: %w", err)
	}
	return out, nil
}

// HostStatus reports whether an agent host claimed work within HostLapse, and when one last did.
func HostStatus(ctx context.Context, q storage.Querier) (connected bool, seenAt *time.Time, err error) {
	err = q.QueryRow(ctx, `SELECT max(seen_at), coalesce(max(seen_at) > now() - $1::interval, false) FROM agent_hosts`,
		fmt.Sprintf("%d milliseconds", HostLapse.Milliseconds())).Scan(&seenAt, &connected)
	if err != nil {
		return false, nil, fmt.Errorf("agent host status: %w", err)
	}
	return connected, seenAt, nil
}

// DefaultModel returns the opencode model the admin chose for new projects, if any.
func DefaultModel(ctx context.Context, q storage.Querier, agent string) (string, bool, error) {
	var m string
	err := q.QueryRow(ctx, `SELECT default_model FROM agent_credentials
		WHERE agent = $1 AND default_model IS NOT NULL AND archived_at IS NULL LIMIT 1`, agent).Scan(&m)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the default %s model: %w", agent, err)
	}
	return m, true, nil
}

// Models returns the verified models of the live credentials of agent, in credential order.
func Models(ctx context.Context, q storage.Querier, agent string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT models FROM agent_credentials WHERE agent = $1 AND archived_at IS NULL
		AND verify_state = 'ok' ORDER BY name, id`, agent)
	if err != nil {
		return nil, fmt.Errorf("list %s models: %w", agent, err)
	}
	lists, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, fmt.Errorf("list %s models: %w", agent, err)
	}
	var out []string
	for _, raw := range lists {
		var ms []string
		if err := json.Unmarshal(raw, &ms); err != nil {
			return nil, fmt.Errorf("decode models: %w", err)
		}
		for _, m := range ms {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// EgressHosts is the allowlist the egress proxy adds to its static list: the hosts of every live credential.
func EgressHosts(ctx context.Context, q storage.Querier) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT catalogue_id, coalesce(base_url, '') FROM agent_credentials WHERE archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("egress hosts: %w", err)
	}
	pairs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
		var p [2]string
		return p, r.Scan(&p[0], &p[1])
	})
	if err != nil {
		return nil, fmt.Errorf("egress hosts: %w", err)
	}
	all := make([][]string, 0, len(pairs))
	for _, p := range pairs {
		all = append(all, HostsOf(p[0], p[1]))
	}
	return Allowlist(all...), nil
}

// ---------------------------------------------------------------- commands

// SetInput is the body of agentCredentials.set. Value is nil when the stored one stays.
type SetInput struct {
	ID           string
	Value        []byte
	Name         *string
	CatalogueID  string
	BaseURL      *string
	DefaultModel *string
	// Rev is the If-Match revision; 0 when absent (allowed only while the credential does not exist or is archived).
	Rev    int
	Actor  auth.Actor
	DryRun bool
}

func lock(ctx context.Context, tx pgx.Tx, id string) (Credential, bool, error) {
	rows, err := tx.Query(ctx, "SELECT "+cols+" FROM agent_credentials WHERE id = $1 FOR UPDATE", id)
	c, err := one(rows, err, id)
	var pe *problems.Error
	if errors.As(err, &pe) && pe.Type == problems.NotFound {
		return Credential{}, false, nil
	}
	return c, err == nil, err
}

func bad(path, format string, a ...any) error {
	return problems.Validation([]problems.FieldError{{Path: path, Message: fmt.Sprintf(format, a...)}})
}

// Set is agentCredentials.set: it creates, revives or changes a credential. A new value is sealed into the transit
// store (unless dryRun) under a new `write` task that supersedes any older write of the same credential; a new base
// URL or name of a custom provider is a write without a value (the provider block changes, the key stays).
// DefaultModel makes the credential opencode's default for new projects.
func Set(ctx context.Context, tx pgx.Tx, transit Transit, in SetInput) (Credential, []events.Draft, error) {
	agent, provider, ok := ParseID(in.ID)
	if !ok {
		return Credential{}, nil, bad("/id", "use claude-code or opencode.<provider> (lowercase letters, digits and dashes)")
	}
	cur, exists, err := lock(ctx, tx, in.ID)
	if err != nil {
		return Credential{}, nil, err
	}
	live := exists && cur.Live()
	if exists && in.Rev != 0 {
		if err := commands.CheckRev("agent credential", in.Rev, cur.Rev); err != nil {
			return Credential{}, nil, err
		}
	} else if live {
		return Credential{}, nil, commands.Precondition("agent credential")
	}
	entry := CatalogueFor(agent, provider)
	if in.CatalogueID != "" && in.CatalogueID != entry.ID {
		return Credential{}, nil, bad("/catalogueId", "%s is configured from the %q catalogue entry, not %q", in.ID, entry.ID, in.CatalogueID)
	}
	next := cur
	if !live {
		next = Credential{ID: in.ID, Agent: agent, Provider: provider, CatalogueID: entry.ID, Name: entry.Name, Models: []string{}}
		if entry.Custom {
			next.Name = provider
		}
	}
	write := false
	var value string
	if in.Value != nil {
		value = strings.TrimSpace(string(in.Value))
		if err := CheckValue(entry, value); err != nil {
			return Credential{}, nil, bad("/value", "the value %v", err)
		}
		write = true
	} else if !live && entry.KeyRequired {
		return Credential{}, nil, bad("/value", "required: paste the %s", entry.KeyLabel)
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			return Credential{}, nil, bad("/name", "must not be empty")
		}
		if n != next.Name && entry.Custom {
			write = true // the name is part of opencode's provider block
		}
		next.Name = n
	}
	if in.BaseURL != nil {
		if !entry.Custom {
			return Credential{}, nil, bad("/baseUrl", "only a custom (OpenAI-compatible) provider takes a base URL")
		}
		u, err := ParseBaseURL(*in.BaseURL)
		if err != nil {
			return Credential{}, nil, bad("/baseUrl", "the base URL %v", err)
		}
		if s := strings.TrimSuffix(u.String(), "/"); s != next.BaseURL {
			next.BaseURL, write = s, true
		}
	}
	if entry.Custom && next.BaseURL == "" {
		return Credential{}, nil, bad("/baseUrl", "required for a custom provider, e.g. http://vllm.lan:8000/v1")
	}
	if !live && entry.Custom && !write {
		write = true // a custom provider without a key still needs its provider block
	}
	if in.DefaultModel != nil {
		m := strings.TrimSpace(*in.DefaultModel)
		switch {
		case agent != AgentOpencode:
			return Credential{}, nil, bad("/defaultModel", "Claude Code's model is chosen per project (sonnet, opus, haiku)")
		case !strings.HasPrefix(m, provider+"/") || len(m) == len(provider)+1:
			return Credential{}, nil, bad("/defaultModel", "use a model of this provider: %s/<model>", provider)
		case len(next.Models) > 0 && !slices.Contains(next.Models, m):
			return Credential{}, nil, bad("/defaultModel", "%s is not among the provider's models from the last verification", m)
		}
		next.DefaultModel = m
	}
	if !live && !write && in.DefaultModel == nil {
		return Credential{}, nil, bad("/value", "nothing to set")
	}
	now := time.Now().UTC()
	if write && in.Value != nil {
		next.HasValue, next.Hint, next.SetAt, next.SetBy = true, Mask(value), &now, &in.Actor
		next.ExpiresAt = nil
		if agent == AgentClaude {
			exp := now.Add(ClaudeTokenLifetime)
			next.ExpiresAt = &exp
		}
	}
	if write {
		next.Delivery = State{State: DeliveryPending}
		next.Verification = State{State: VerifyNone}
	}
	setBy, err := json.Marshal(next.SetBy)
	if err != nil {
		return Credential{}, nil, fmt.Errorf("encode set_by: %w", err)
	}
	models, err := json.Marshal(next.Models)
	if err != nil {
		return Credential{}, nil, fmt.Errorf("encode models: %w", err)
	}
	if next.DefaultModel != "" && in.DefaultModel != nil {
		if _, err := tx.Exec(ctx, `UPDATE agent_credentials SET default_model = NULL, rev = rev + 1, updated_at = now()
			WHERE agent = $1 AND id <> $2 AND default_model IS NOT NULL`, agent, in.ID); err != nil {
			return Credential{}, nil, fmt.Errorf("clear the old default model: %w", err)
		}
	}
	rows, err := tx.Query(ctx, `INSERT INTO agent_credentials (id, agent, provider, catalogue_id, name, base_url, has_value, hint,
			set_at, set_by, expires_at, delivery_state, delivery_detail, delivery_at, verify_state, verify_detail, verify_model,
			verified_at, models, default_model)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9, $10, $11, $12, '', NULL, $13, '', '', NULL, $14, nullif($15, ''))
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, base_url = excluded.base_url, has_value = excluded.has_value,
			hint = excluded.hint, set_at = excluded.set_at, set_by = excluded.set_by, expires_at = excluded.expires_at,
			delivery_state = CASE WHEN $16 THEN excluded.delivery_state ELSE agent_credentials.delivery_state END,
			delivery_detail = CASE WHEN $16 THEN '' ELSE agent_credentials.delivery_detail END,
			delivery_at = CASE WHEN $16 THEN NULL ELSE agent_credentials.delivery_at END,
			verify_state = CASE WHEN $16 THEN excluded.verify_state ELSE agent_credentials.verify_state END,
			verify_detail = CASE WHEN $16 THEN '' ELSE agent_credentials.verify_detail END,
			verify_model = CASE WHEN $16 THEN '' ELSE agent_credentials.verify_model END,
			verified_at = CASE WHEN $16 THEN NULL ELSE agent_credentials.verified_at END,
			models = excluded.models, default_model = excluded.default_model, archived_at = NULL,
			rev = agent_credentials.rev + 1, updated_at = now()
		RETURNING `+cols,
		next.ID, next.Agent, next.Provider, next.CatalogueID, next.Name, next.BaseURL, next.HasValue, next.Hint, next.SetAt,
		setBy, next.ExpiresAt, next.Delivery.State, next.Verification.State, models, next.DefaultModel, write)
	saved, err := one(rows, err, in.ID)
	if err != nil {
		return Credential{}, nil, err
	}
	if write {
		task, err := newTask(ctx, tx, saved.ID, ActionWrite, in.Value != nil)
		if err != nil {
			return Credential{}, nil, err
		}
		if in.Value != nil && !in.DryRun {
			if transit == nil {
				return Credential{}, nil, errors.New("agent credentials: no secret store to carry the value")
			}
			if err := transit.SealTransit(task, []byte(value)); err != nil {
				return Credential{}, nil, err
			}
		}
	}
	return saved, event(saved, "agent_credential.set"), nil
}

// Verify is agentCredentials.verify: a `verify` task the agent host runs as a sandboxed session user.
func Verify(ctx context.Context, tx pgx.Tx, id string, rev int) (Credential, []events.Draft, error) {
	cur, exists, err := lock(ctx, tx, id)
	if err != nil {
		return Credential{}, nil, err
	}
	if !exists {
		return Credential{}, nil, problems.NotFound.New("no agent credential %q (agentCredentials.list)", id)
	}
	if err := commands.CheckRev("agent credential", rev, cur.Rev); err != nil {
		return Credential{}, nil, err
	}
	if !cur.Live() {
		return Credential{}, nil, problems.Conflict.New("%s is disconnected; set it again before verifying", id)
	}
	if _, err := newTask(ctx, tx, id, ActionVerify, false); err != nil {
		return Credential{}, nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE agent_credentials SET verify_state = 'pending', verify_detail = '', rev = rev + 1,
		updated_at = now() WHERE id = $1 RETURNING `+cols, id)
	next, err := one(rows, err, id)
	if err != nil {
		return Credential{}, nil, err
	}
	return next, event(next, "agent_credential.verify_requested"), nil
}

// Archive is agentCredentials.archive: the credential is disconnected now (no longer offered, off the egress
// allowlist, no longer the default) and a `remove` task deletes it from the volume.
func Archive(ctx context.Context, tx pgx.Tx, id string, rev int) (Credential, []events.Draft, error) {
	cur, exists, err := lock(ctx, tx, id)
	if err != nil {
		return Credential{}, nil, err
	}
	if !exists {
		return Credential{}, nil, problems.NotFound.New("no agent credential %q (agentCredentials.list)", id)
	}
	if err := commands.CheckRev("agent credential", rev, cur.Rev); err != nil {
		return Credential{}, nil, err
	}
	if !cur.Live() {
		return Credential{}, nil, problems.Conflict.New("%s is already disconnected", id)
	}
	if _, err := newTask(ctx, tx, id, ActionRemove, false); err != nil {
		return Credential{}, nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE agent_credentials SET archived_at = now(), delivery_state = 'removing',
		delivery_detail = '', delivery_at = NULL, verify_state = 'none', verify_detail = '', default_model = NULL,
		rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING `+cols, id)
	next, err := one(rows, err, id)
	if err != nil {
		return Credential{}, nil, err
	}
	return next, event(next, "agent_credential.archived"), nil
}

// newTask inserts a task for credential id. A write supersedes open writes and verifies (their transit values become
// orphans the sweeper drops), a remove supersedes open writes and verifies too, a verify supersedes open verifies. A
// remove is never superseded: a credential set again after a disconnect is removed first, then written.
func newTask(ctx context.Context, tx pgx.Tx, id, action string, transit bool) (string, error) {
	supersede := `action = 'verify'`
	if action != ActionVerify {
		supersede = `action IN ('write', 'verify')`
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_credential_tasks SET done_at = now(), outcome = 'superseded'
		WHERE credential_id = $1 AND done_at IS NULL AND `+supersede, id); err != nil {
		return "", fmt.Errorf("supersede tasks of %s: %w", id, err)
	}
	task := "act_" + uuid.Must(uuid.NewV7()).String()
	if _, err := tx.Exec(ctx, `INSERT INTO agent_credential_tasks (id, credential_id, action, transit) VALUES ($1, $2, $3, $4)`,
		task, id, action, transit); err != nil {
		return "", fmt.Errorf("queue %s of %s: %w", action, id, err)
	}
	return task, nil
}

// Summary is what an event says about a credential: never the value, the hint or the models.
type Summary struct {
	ID           string `json:"id"`
	Agent        string `json:"agent"`
	Provider     string `json:"provider"`
	Rev          int    `json:"rev"`
	Delivery     State  `json:"delivery"`
	Verification State  `json:"verification"`
}

func event(c Credential, typ string) []events.Draft {
	return []events.Draft{{
		Topic:  events.EntityTopic(Kind, c.ID),
		Type:   typ,
		Entity: &events.EntityRef{Kind: Kind, ID: c.ID, Rev: c.Rev},
		Payload: map[string]any{"agentCredential": Summary{
			ID: c.ID, Agent: c.Agent, Provider: c.Provider, Rev: c.Rev,
			Delivery:     State{State: c.Delivery.State},
			Verification: State{State: c.Verification.State},
		}},
	}}
}
