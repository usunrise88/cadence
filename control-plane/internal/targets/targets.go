// Package targets keeps deployment targets (docs/spec/02-domain-projects-registry.md "Deployment targets", R46):
// where models are served, instance-wide like mounts and compute. A staging target is the server Cadence reaches; a
// delivery target is a production server only a person's delivery script reaches, so it never has an endpoint (no
// code connects to it, non-negotiable 8). Creating or changing a target is a registry approval for everyone (preset
// rule deployment-targets); the approved replay runs Create or Edit, whose hooks append a delivery target's genesis
// and target-changed records (internal/promotions).
//
// TODO(D2): the staging target's health check, serving.* beyond staging_target, and served-model lease counting
// build on this table (stream D2 extends it; the payload and the hooks stay).
package targets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the entity kind of a deployment target; its events are on entity.deployment_target.{id}.
const Kind = "deployment_target"

// Operations, as the policy engine and the audit log name them.
const (
	OpNew     = "deploymentTargets.new"
	OpEdit    = "deploymentTargets.edit"
	OpArchive = "deploymentTargets.archive"
)

// Target kinds.
const (
	KindStaging  = "staging"
	KindDelivery = "delivery"
)

// States.
const (
	StateActive   = "active"
	StateArchived = "archived"
)

// Event types on Topic(id).
const (
	EventCreated  = "deployment_target.created"
	EventChanged  = "deployment_target.changed"
	EventArchived = "deployment_target.archived"
)

// Topic is the topic of one target.
func Topic(id string) string { return events.EntityTopic(Kind, id) }

// Serves is one model family a target serves (compared as data, R46): its deployable formats and latency profiles,
// the first profile being the primary one.
type Serves struct {
	Family   string   `json:"family"`
	Formats  []string `json:"formats"`
	Profiles []string `json:"profiles"`
}

// Server is the inference server kind and version.
type Server struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

// Boost is what the target accepts of boosting (R32 · confirm).
type Boost struct {
	Static          *bool `json:"static,omitempty"`
	Dynamic         *bool `json:"dynamic,omitempty"`
	MaxTermsPerCall *int  `json:"maxTermsPerCall,omitempty"`
}

// Config is what a target is. Serves, Server, RepositoryPath and Slots are named by promotion records.
type Config struct {
	Serves         []Serves `json:"serves"`
	Server         Server   `json:"server"`
	Endpoint       string   `json:"endpoint,omitempty"`
	RepositoryPath string   `json:"repositoryPath,omitempty"`
	Slots          []string `json:"slots"`
	Concurrency    int      `json:"concurrency,omitempty"`
	CardClass      string   `json:"cardClass,omitempty"`
	Boost          *Boost   `json:"boost,omitempty"`
}

// Target is one row; its JSON form is the contract's DeploymentTarget (without chain, which the server adds).
type Target struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Config
	State      string     `json:"state"`
	ApprovalID string     `json:"approvalId,omitempty"`
	Rev        int        `json:"rev"`
	CreatedBy  auth.Actor `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
}

// RecordKeys are the payload keys promotion records name; a change to one appends a target-changed record.
var RecordKeys = []string{"serves", "server", "repositoryPath", "slots", "concurrency", "cardClass", "boost"}

// Payload is the target as a promotion record names it: its name and kind and the RecordKeys present.
func (t Target) Payload() map[string]any {
	b, _ := json.Marshal(t.Config)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	out := map[string]any{"id": t.ID, "name": t.Name, "kind": t.Kind}
	for _, k := range RecordKeys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Serving reports whether the target serves family in format at profile (the promotion check, R46).
func (t Target) Serving(family, format, profile string) bool {
	for _, s := range t.Serves {
		if s.Family == family && slices.Contains(s.Formats, format) && slices.Contains(s.Profiles, profile) {
			return true
		}
	}
	return false
}

// HasSlot reports whether the target names slot.
func (t Target) HasSlot(slot string) bool { return slices.Contains(t.Slots, slot) }

const cols = `id, name, kind, description, config, state, coalesce(approval_id, ''), rev, created_by, created_at,
	updated_at, archived_at`

func scan(row pgx.CollectableRow) (Target, error) {
	var (
		t   Target
		cfg []byte
	)
	if err := row.Scan(&t.ID, &t.Name, &t.Kind, &t.Description, &cfg, &t.State, &t.ApprovalID, &t.Rev, &t.CreatedBy,
		&t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt); err != nil {
		return Target{}, err
	}
	if err := json.Unmarshal(cfg, &t.Config); err != nil {
		return Target{}, fmt.Errorf("target %s: config: %w", t.ID, err)
	}
	if t.Serves == nil {
		t.Serves = []Serves{}
	}
	if t.Slots == nil {
		t.Slots = []string{}
	}
	return t, nil
}

// List returns targets by name; archived ones only with all.
func List(ctx context.Context, q storage.Querier, all bool) ([]Target, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM deployment_targets WHERE $1 OR state = 'active' ORDER BY name", all)
	if err != nil {
		return nil, fmt.Errorf("list deployment targets: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("list deployment targets: %w", err)
	}
	return out, nil
}

// Get reads a target by id (dtg_…) or name.
func Get(ctx context.Context, q storage.Querier, ref string) (Target, error) {
	return get(ctx, q, ref, false)
}

// Lock reads a target by id or name and locks its row for the transaction: every append to its chain holds it.
func Lock(ctx context.Context, tx pgx.Tx, ref string) (Target, error) {
	return get(ctx, tx, ref, true)
}

func get(ctx context.Context, q storage.Querier, ref string, lock bool) (Target, error) {
	sql := "SELECT " + cols + " FROM deployment_targets WHERE id = $1 OR name = $1"
	if lock {
		sql += " FOR UPDATE"
	}
	rows, err := q.Query(ctx, sql, ref)
	if err != nil {
		return Target{}, fmt.Errorf("read deployment target: %w", err)
	}
	t, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, problems.NotFound.New("no deployment target %q (deploymentTargets.list names them)", ref)
	}
	if err != nil {
		return Target{}, fmt.Errorf("read deployment target: %w", err)
	}
	return t, nil
}

// Hook runs in the transaction that created a target; its events join the command's.
type Hook func(ctx context.Context, tx pgx.Tx, t Target) ([]events.Draft, error)

// ChangeHook runs in the transaction of an approved edit with the target before and after and the RecordKeys that
// changed (none: only the description).
type ChangeHook func(ctx context.Context, tx pgx.Tx, before, after Target, changed []string) ([]events.Draft, error)

// Service creates, edits and archives targets.
type Service struct {
	// ServerKinds are the server kinds the delivery script can install into (templates/delivery/servers/<kind>.sh);
	// a delivery target of another kind is refused. Nil accepts any.
	ServerKinds []string
	onCreated   []Hook
	onChanged   []ChangeHook
}

// OnCreated adds a hook that runs when a target is created (the genesis record of a delivery target).
func (s *Service) OnCreated(h Hook) { s.onCreated = append(s.onCreated, h) }

// OnChanged adds a hook that runs when a target is edited (the target-changed record).
func (s *Service) OnChanged(h ChangeHook) { s.onChanged = append(s.onChanged, h) }

// Input is the body of deploymentTargets.new.
type Input struct {
	Name, Kind, Description string
	Config
}

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	slotRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
	serverRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	// repoPathRe keeps the path the delivery script installs into free of shell metacharacters and spaces.
	repoPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,498}$`)
)

// Validate checks a target as it would be stored and returns it (id empty, rev 1).
func (s *Service) Validate(ctx context.Context, q storage.Querier, in Input) (Target, error) {
	t := Target{Name: in.Name, Kind: in.Kind, Description: in.Description, Config: in.Config, State: StateActive, Rev: 1}
	if t.Serves == nil {
		t.Serves = []Serves{}
	}
	if t.Slots == nil {
		t.Slots = []string{}
	}
	var fields []problems.FieldError
	bad := func(p, msg string, args ...any) {
		fields = append(fields, problems.FieldError{Path: p, Message: fmt.Sprintf(msg, args...)})
	}
	if !nameRe.MatchString(t.Name) {
		bad("/name", "lower-case letters, digits and dashes, at most 40, starting and ending with a letter or digit")
	}
	if t.Kind != KindStaging && t.Kind != KindDelivery {
		bad("/kind", "staging or delivery")
	}
	s.validateConfig(t, bad)
	if len(fields) > 0 {
		return Target{}, problems.Validation(fields)
	}
	if q != nil {
		var taken bool
		if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM deployment_targets WHERE name = $1)", t.Name).Scan(&taken); err != nil {
			return Target{}, fmt.Errorf("check target name: %w", err)
		}
		if taken {
			return Target{}, problems.Conflict.New("a deployment target named %q exists; names never change, so pick another", t.Name)
		}
	}
	return t, nil
}

func (s *Service) validateConfig(t Target, bad func(p, msg string, args ...any)) {
	if !serverRe.MatchString(t.Server.Kind) {
		bad("/server/kind", "lower-case letters, digits and dashes, starting with a letter")
	}
	if strings.TrimSpace(t.Server.Version) == "" {
		bad("/server/version", "name the server's version")
	}
	families := map[string]bool{}
	for i, sv := range t.Serves {
		if strings.TrimSpace(sv.Family) == "" {
			bad(fmt.Sprintf("/serves/%d/family", i), "name a model family")
		}
		if families[sv.Family] {
			bad(fmt.Sprintf("/serves/%d/family", i), "family %q is listed twice; list its formats and profiles once", sv.Family)
		}
		families[sv.Family] = true
		if len(sv.Formats) == 0 {
			bad(fmt.Sprintf("/serves/%d/formats", i), "at least one deployable format")
		}
		if len(sv.Profiles) == 0 {
			bad(fmt.Sprintf("/serves/%d/profiles", i), "at least one latency profile (the first is the primary one)")
		}
	}
	seen := map[string]bool{}
	for i, sl := range t.Slots {
		if !slotRe.MatchString(sl) {
			bad(fmt.Sprintf("/slots/%d", i), "lower-case letters, digits, dots, dashes and underscores (a model name the server accepts)")
		}
		if seen[sl] {
			bad(fmt.Sprintf("/slots/%d", i), "slot %q is listed twice", sl)
		}
		seen[sl] = true
	}
	if t.Concurrency < 0 || t.Concurrency > 512 {
		bad("/concurrency", "between 1 and 512 streams")
	}
	switch t.Kind {
	case KindDelivery:
		if t.Endpoint != "" {
			bad("/endpoint", "a delivery target has no endpoint: Cadence never reaches a production host; the delivery script runs there")
		}
		if !repoPathRe.MatchString(t.RepositoryPath) || slices.Contains(strings.Split(t.RepositoryPath, "/"), "..") ||
			path.Clean(t.RepositoryPath) != t.RepositoryPath {
			bad("/repositoryPath", "the absolute, clean path on the production host where model directories are installed (letters, digits, . _ - /)")
		}
		if len(t.Slots) == 0 {
			bad("/slots", "a delivery target names at least one slot: the model name the production pipeline calls")
		}
		if len(t.Serves) == 0 {
			bad("/serves", "a delivery target serves at least one model family, format and profile")
		}
		if s.ServerKinds != nil && !slices.Contains(s.ServerKinds, t.Server.Kind) {
			bad("/server/kind", "the delivery script has no functions for server kind %q (it knows %s)", t.Server.Kind, strings.Join(s.ServerKinds, ", "))
		}
	case KindStaging:
		u, err := url.Parse(t.Endpoint)
		if t.Endpoint == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("/endpoint", "a staging target names the http(s) endpoint of the server Cadence reaches")
		}
		if t.RepositoryPath != "" && !repoPathRe.MatchString(t.RepositoryPath) {
			bad("/repositoryPath", "an absolute path (letters, digits, . _ - /)")
		}
	}
}

// Create inserts a validated target and runs the OnCreated hooks.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, in Input, actor auth.Actor, approvalID string, now time.Time) (Target, []events.Draft, error) {
	t, err := s.Validate(ctx, tx, in)
	if err != nil {
		return Target{}, nil, err
	}
	t.ID = "dtg_" + uuid.Must(uuid.NewV7()).String()
	cfg, err := json.Marshal(t.Config)
	if err != nil {
		return Target{}, nil, fmt.Errorf("encode target config: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deployment_targets (id, name, kind, description, config, created_by, approval_id,
			created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $8)`,
		t.ID, t.Name, t.Kind, t.Description, cfg, actor, approvalID, now); err != nil {
		return Target{}, nil, fmt.Errorf("create deployment target: %w", err)
	}
	if t, err = Get(ctx, tx, t.ID); err != nil {
		return Target{}, nil, err
	}
	drafts := []events.Draft{{Topic: Topic(t.ID), Type: EventCreated, Entity: &events.EntityRef{Kind: Kind, ID: t.ID, Rev: t.Rev},
		Payload: map[string]any{"id": t.ID, "name": t.Name, "kind": t.Kind}}}
	for _, h := range s.onCreated {
		more, err := h(ctx, tx, t)
		if err != nil {
			return Target{}, nil, err
		}
		drafts = append(drafts, more...)
	}
	return t, drafts, nil
}

// Edit is the body of deploymentTargets.edit: nil fields stay.
type Edit struct {
	Description    *string
	Serves         *[]Serves
	Server         *Server
	Endpoint       *string
	RepositoryPath *string
	Slots          *[]string
	Concurrency    *int
	CardClass      *string
	Boost          *Boost
}

// Apply returns t with e applied, validated, and the RecordKeys that changed.
func (s *Service) Apply(t Target, e Edit) (Target, []string, error) {
	if t.State == StateArchived {
		return Target{}, nil, problems.Conflict.New("deployment target %s is archived; it takes no changes", t.Name)
	}
	before := t.Payload()
	n := t
	n.Serves = slices.Clone(t.Serves)
	n.Slots = slices.Clone(t.Slots)
	if e.Description != nil {
		n.Description = *e.Description
	}
	if e.Serves != nil {
		n.Serves = *e.Serves
	}
	if e.Server != nil {
		n.Server = *e.Server
	}
	if e.Endpoint != nil {
		n.Endpoint = *e.Endpoint
	}
	if e.RepositoryPath != nil {
		n.RepositoryPath = *e.RepositoryPath
	}
	if e.Slots != nil {
		n.Slots = *e.Slots
	}
	if e.Concurrency != nil {
		n.Concurrency = *e.Concurrency
	}
	if e.CardClass != nil {
		n.CardClass = *e.CardClass
	}
	if e.Boost != nil {
		n.Boost = e.Boost
	}
	var fields []problems.FieldError
	s.validateConfig(n, func(p, msg string, args ...any) {
		fields = append(fields, problems.FieldError{Path: p, Message: fmt.Sprintf(msg, args...)})
	})
	if len(fields) > 0 {
		return Target{}, nil, problems.Validation(fields)
	}
	after := n.Payload()
	var changed []string
	for _, k := range RecordKeys {
		if !reflect.DeepEqual(before[k], after[k]) {
			changed = append(changed, k)
		}
	}
	return n, changed, nil
}

// Save writes an applied edit (rev + 1) and runs the OnChanged hooks.
func (s *Service) Save(ctx context.Context, tx pgx.Tx, before, after Target, changed []string, now time.Time) (Target, []events.Draft, error) {
	cfg, err := json.Marshal(after.Config)
	if err != nil {
		return Target{}, nil, fmt.Errorf("encode target config: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE deployment_targets SET description = $2, config = $3, rev = rev + 1, updated_at = $4
		WHERE id = $1 AND rev = $5`, before.ID, after.Description, cfg, now, before.Rev)
	if err != nil {
		return Target{}, nil, fmt.Errorf("edit deployment target: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return Target{}, nil, problems.Conflict.New("deployment target %s changed meanwhile; read it again", before.Name)
	}
	t, err := Get(ctx, tx, before.ID)
	if err != nil {
		return Target{}, nil, err
	}
	drafts := []events.Draft{{Topic: Topic(t.ID), Type: EventChanged, Entity: &events.EntityRef{Kind: Kind, ID: t.ID, Rev: t.Rev},
		Payload: map[string]any{"id": t.ID, "name": t.Name, "changed": changed}}}
	for _, h := range s.onChanged {
		more, err := h(ctx, tx, before, t, changed)
		if err != nil {
			return Target{}, nil, err
		}
		drafts = append(drafts, more...)
	}
	return t, drafts, nil
}

// Archive marks a target archived (soft): it takes no new deployments or promotions; its chain stays.
func Archive(ctx context.Context, tx pgx.Tx, t Target, now time.Time) (Target, []events.Draft, error) {
	if t.State == StateArchived {
		return t, nil, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE deployment_targets SET state = 'archived', archived_at = $2, updated_at = $2, rev = rev + 1
		WHERE id = $1`, t.ID, now); err != nil {
		return Target{}, nil, fmt.Errorf("archive deployment target: %w", err)
	}
	a, err := Get(ctx, tx, t.ID)
	if err != nil {
		return Target{}, nil, err
	}
	return a, []events.Draft{{Topic: Topic(a.ID), Type: EventArchived, Entity: &events.EntityRef{Kind: Kind, ID: a.ID, Rev: a.Rev},
		Payload: map[string]any{"id": a.ID, "name": a.Name}}}, nil
}

// Seed creates the staging target defaults.yaml names (serving.staging_target) when no target of that name exists.
// It reports whether it created one.
func Seed(ctx context.Context, pool *pgxpool.Pool, name, endpoint string, server Server, now time.Time) (bool, error) {
	if name == "" {
		return false, nil
	}
	cfg, err := json.Marshal(Config{Serves: []Serves{}, Server: server, Endpoint: endpoint, Slots: []string{}})
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `INSERT INTO deployment_targets (id, name, kind, config, created_by, created_at, updated_at)
		VALUES ($1, $2, 'staging', $3, $4, $5, $5) ON CONFLICT (name) DO NOTHING`,
		"dtg_"+uuid.Must(uuid.NewV7()).String(), name, cfg, auth.Actor{Kind: auth.KindAutomation, ID: "cadence", Name: "Cadence"}, now)
	if err != nil {
		return false, fmt.Errorf("seed the staging target: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
