// Package policies holds the instance-wide policies (docs/spec/06-platform.md; ROADMAP phase 1 "Policies"):
// default budgets now, retention, PII and cache quotas in phases 4–5. The single row stores only the values set by
// policies.edit; everything else reads through from defaults.yaml, so a changed default reaches every instance
// that did not depart from it, and departures are listed.
package policies

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the EntityKind of the policies singleton.
const Kind = "policies"

// ID is the id of the one policies row.
const ID = "instance"

// Budgets are the instance-wide default budgets. Its JSON form is the contract's PolicyBudgets.
type Budgets struct {
	GPUHoursPerProjectPerDay float64 `json:"gpuHoursPerProjectPerDay"`
	AgentTurnsPerSession     int     `json:"agentTurnsPerSession"`
}

// Policies are the effective policies. Its JSON form is the contract's Policies.
type Policies struct {
	Rev        int       `json:"rev"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Budgets    Budgets   `json:"budgets"`
	Departures []string  `json:"departures"`
}

// overrides is the stored form: only what policies.edit set.
type overrides struct {
	GPUHoursPerProjectPerDay *float64 `json:"gpuHoursPerProjectPerDay,omitempty"`
	AgentTurnsPerSession     *int     `json:"agentTurnsPerSession,omitempty"`
}

// Get returns the effective policies: stored values over defaults d.
func Get(ctx context.Context, q storage.Querier, d *defaults.Defaults) (Policies, error) {
	p, o, err := load(ctx, q, "")
	if err != nil {
		return Policies{}, err
	}
	return effective(p, o, d), nil
}

// load reads the row: revision and time in p, the stored values in o.
func load(ctx context.Context, q storage.Querier, lock string) (p Policies, o overrides, _ error) {
	var raw []byte
	if err := q.QueryRow(ctx, "SELECT rev, updated_at, budgets FROM policies WHERE id = $1 "+lock, ID).
		Scan(&p.Rev, &p.UpdatedAt, &raw); err != nil {
		return p, o, fmt.Errorf("read policies: %w", err)
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return p, o, fmt.Errorf("decode policies: %w", err)
	}
	return p, o, nil
}

func effective(p Policies, o overrides, d *defaults.Defaults) Policies {
	p.Budgets = Budgets{
		GPUHoursPerProjectPerDay: d.Budgets.GPUHoursPerProjectPerDay.Value,
		AgentTurnsPerSession:     d.Budgets.AgentTurnsPerSession.Value,
	}
	p.Departures = []string{}
	if o.GPUHoursPerProjectPerDay != nil {
		p.Budgets.GPUHoursPerProjectPerDay = *o.GPUHoursPerProjectPerDay
	}
	if o.AgentTurnsPerSession != nil {
		p.Budgets.AgentTurnsPerSession = *o.AgentTurnsPerSession
	}
	if p.Budgets.GPUHoursPerProjectPerDay != d.Budgets.GPUHoursPerProjectPerDay.Value {
		p.Departures = append(p.Departures, "budgets.gpuHoursPerProjectPerDay")
	}
	if p.Budgets.AgentTurnsPerSession != d.Budgets.AgentTurnsPerSession.Value {
		p.Departures = append(p.Departures, "budgets.agentTurnsPerSession")
	}
	return p
}

// EditInput is the body of policies.edit; nil fields stay as they are.
type EditInput struct {
	GPUHoursPerProjectPerDay *float64
	AgentTurnsPerSession     *int
}

// Edit changes the policies at revision rev. Values must lie inside the ranges defaults.yaml gives them.
func Edit(ctx context.Context, tx pgx.Tx, rev int, in EditInput, d *defaults.Defaults) (Policies, []events.Draft, error) {
	cur, o, err := load(ctx, tx, "FOR UPDATE")
	if err != nil {
		return Policies{}, nil, err
	}
	if err := commands.CheckRev(Kind, rev, cur.Rev); err != nil {
		return Policies{}, nil, err
	}
	var fields []problems.FieldError
	if v := in.GPUHoursPerProjectPerDay; v != nil {
		if err := d.Budgets.GPUHoursPerProjectPerDay.Range.Check(*v); err != nil {
			fields = append(fields, problems.FieldError{Path: "/budgets/gpuHoursPerProjectPerDay", Message: err.Error() + " (defaults.yaml budgets.gpu_hours_per_project_per_day)"})
		}
		o.GPUHoursPerProjectPerDay = v
	}
	if v := in.AgentTurnsPerSession; v != nil {
		if err := d.Budgets.AgentTurnsPerSession.Range.Check(float64(*v)); err != nil {
			fields = append(fields, problems.FieldError{Path: "/budgets/agentTurnsPerSession", Message: err.Error() + " (defaults.yaml budgets.agent_turns_per_session)"})
		}
		o.AgentTurnsPerSession = v
	}
	if len(fields) > 0 {
		return Policies{}, nil, problems.Validation(fields)
	}
	stored, err := json.Marshal(o)
	if err != nil {
		return Policies{}, nil, fmt.Errorf("encode policies: %w", err)
	}
	var p Policies
	if err := tx.QueryRow(ctx, `UPDATE policies SET budgets = $2, rev = rev + 1, updated_at = now() WHERE id = $1
		RETURNING rev, updated_at`, ID, stored).Scan(&p.Rev, &p.UpdatedAt); err != nil {
		return Policies{}, nil, fmt.Errorf("update policies: %w", err)
	}
	p = effective(p, o, d)
	return p, []events.Draft{{
		Topic:   events.EntityTopic(Kind, ID),
		Type:    "policies.edited",
		Entity:  &events.EntityRef{Kind: Kind, ID: ID, Rev: p.Rev},
		Payload: map[string]any{"policies": p},
	}}, nil
}
