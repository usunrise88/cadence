package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// ProfileKind is the entity kind of an agent profile; its id is the project's.
const ProfileKind = "agent_profile"

// Agent drivers.
const (
	DriverClaudeCode = "claude-code"
	DriverOpencode   = "opencode"
)

// DraftKinds are the draftable entity kinds a profile's draft policy covers (docs/spec/05-agents.md).
var DraftKinds = []string{"mix", "gate", "note", "language_pack"}

// AgentProfile is a project's agent configuration (without the rendered files, which the repository holds).
type AgentProfile struct {
	ProjectID            string            `json:"-"`
	Driver               string            `json:"driver"`
	Model                string            `json:"model"`
	PermissionPreset     string            `json:"permissionPreset"`
	InstructionsTemplate string            `json:"instructionsTemplate"`
	AutoMerge            string            `json:"autoMerge"`
	DraftPolicy          map[string]string `json:"draftPolicy"`
	Commit               string            `json:"commit,omitempty"`
	Rev                  int               `json:"rev"`
	UpdatedAt            time.Time         `json:"updatedAt"`
}

const profileCols = `project_id, driver, model, permission_preset, instructions_template, auto_merge, draft_policy,
	last_commit, rev, updated_at`

func scanProfile(row pgx.CollectableRow) (AgentProfile, error) {
	var a AgentProfile
	err := row.Scan(&a.ProjectID, &a.Driver, &a.Model, &a.PermissionPreset, &a.InstructionsTemplate, &a.AutoMerge,
		&a.DraftPolicy, &a.Commit, &a.Rev, &a.UpdatedAt)
	return a, err
}

func oneProfile(rows pgx.Rows, err error, projectID string) (AgentProfile, error) {
	if err != nil {
		return AgentProfile{}, fmt.Errorf("query agent profile: %w", err)
	}
	a, err := pgx.CollectExactlyOneRow(rows, scanProfile)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProfile{}, problems.NotFound.New("project %s has no agent profile (it was created before the project wizard)", projectID)
	}
	if err != nil {
		return AgentProfile{}, fmt.Errorf("read agent profile: %w", err)
	}
	return a, nil
}

// GetAgentProfile returns the project's agent profile; the agent-session stream reads driver, model and preset
// from it.
func GetAgentProfile(ctx context.Context, q storage.Querier, projectID string) (AgentProfile, error) {
	rows, err := q.Query(ctx, "SELECT "+profileCols+" FROM agent_profiles WHERE project_id = $1", projectID)
	return oneProfile(rows, err, projectID)
}

// CreateAgentProfile inserts the profile the wizard chose, at revision 1.
func CreateAgentProfile(ctx context.Context, tx pgx.Tx, a AgentProfile) (AgentProfile, error) {
	dp, err := json.Marshal(a.DraftPolicy)
	if err != nil {
		return AgentProfile{}, fmt.Errorf("marshal draft policy: %w", err)
	}
	rows, err := tx.Query(ctx, `INSERT INTO agent_profiles (project_id, driver, model, permission_preset, instructions_template,
			auto_merge, draft_policy) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+profileCols,
		a.ProjectID, a.Driver, a.Model, a.PermissionPreset, a.InstructionsTemplate, a.AutoMerge, dp)
	return oneProfile(rows, err, a.ProjectID)
}

// ProfileEdit changes some fields of a profile; nil fields stay.
type ProfileEdit struct {
	Driver, Model, PermissionPreset, InstructionsTemplate, AutoMerge *string
	DraftPolicy                                                      map[string]string
}

// LockAgentProfile locks the profile and checks it is at revision rev.
func LockAgentProfile(ctx context.Context, tx pgx.Tx, projectID string, rev int) (AgentProfile, error) {
	rows, err := tx.Query(ctx, "SELECT "+profileCols+" FROM agent_profiles WHERE project_id = $1 FOR UPDATE", projectID)
	cur, err := oneProfile(rows, err, projectID)
	if err != nil {
		return AgentProfile{}, err
	}
	return cur, commands.CheckRev(ProfileKind, rev, cur.Rev)
}

// Apply returns cur with the edit applied (nothing is written).
func (e ProfileEdit) Apply(cur AgentProfile) AgentProfile {
	next := cur
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&next.Driver, e.Driver)
	set(&next.Model, e.Model)
	set(&next.PermissionPreset, e.PermissionPreset)
	set(&next.InstructionsTemplate, e.InstructionsTemplate)
	set(&next.AutoMerge, e.AutoMerge)
	if len(e.DraftPolicy) > 0 {
		dp := make(map[string]string, len(cur.DraftPolicy))
		for k, v := range cur.DraftPolicy {
			dp[k] = v
		}
		for k, v := range e.DraftPolicy {
			dp[k] = v
		}
		next.DraftPolicy = dp
	}
	return next
}

// SaveAgentProfile writes next (from Apply on a locked profile) at the next revision with the commit that holds
// its rendered files, and returns its agent_profile.edited event on the project's topic (Agent settings follows
// entity.project.{id}).
func SaveAgentProfile(ctx context.Context, tx pgx.Tx, next AgentProfile, commit string) (AgentProfile, []events.Draft, error) {
	dp, err := json.Marshal(next.DraftPolicy)
	if err != nil {
		return AgentProfile{}, nil, fmt.Errorf("marshal draft policy: %w", err)
	}
	rows, err := tx.Query(ctx, `UPDATE agent_profiles SET driver = $2, model = $3, permission_preset = $4,
			instructions_template = $5, auto_merge = $6, draft_policy = $7, last_commit = coalesce(NULLIF($8, ''), last_commit),
			rev = rev + 1, updated_at = now()
		WHERE project_id = $1 RETURNING `+profileCols,
		next.ProjectID, next.Driver, next.Model, next.PermissionPreset, next.InstructionsTemplate, next.AutoMerge, dp, commit)
	a, err := oneProfile(rows, err, next.ProjectID)
	if err != nil {
		return AgentProfile{}, nil, err
	}
	return a, profileEvent(a, "agent_profile.edited"), nil
}

// SetProfileCommit records the commit holding the rendered files without a new revision (bootstrap, sync).
func SetProfileCommit(ctx context.Context, tx pgx.Tx, projectID, commit string) error {
	if _, err := tx.Exec(ctx, "UPDATE agent_profiles SET last_commit = $2 WHERE project_id = $1", projectID, commit); err != nil {
		return fmt.Errorf("record profile commit: %w", err)
	}
	return nil
}

func profileEvent(a AgentProfile, typ string) []events.Draft {
	return []events.Draft{{
		Topic:     events.EntityTopic(Kind, a.ProjectID),
		Type:      typ,
		ProjectID: a.ProjectID,
		Entity:    &events.EntityRef{Kind: ProfileKind, ID: a.ProjectID, Rev: a.Rev},
		Payload:   map[string]any{"agentProfile": a},
	}}
}
