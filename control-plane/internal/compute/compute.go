// Package compute holds the compute entity: hosts and their cards with the memory cap per card, the job kinds each
// card accepts and the host's health (docs/spec/02-domain-projects-registry.md, entity Compute). Settings edits it
// now; the queue schedules against it from phase 2. Hosts are seeded from defaults.yaml at first start.
package compute

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the EntityKind of a compute host.
const Kind = "compute"

// JobKinds are the kinds of work a card may accept (the contract's JobKind).
var JobKinds = []string{"training", "eval", "shadow", "export"}

// JobTraining is the job kind of training runs.
const JobTraining = "training"

// Card is one card of a host. Its JSON form is the contract's ComputeCard and the stored form.
type Card struct {
	Index           int      `json:"index"`
	Name            string   `json:"name"`
	CardClass       string   `json:"cardClass"`
	MemoryGB        float64  `json:"memoryGb"`
	MemoryCapGB     float64  `json:"memoryCapGb"`
	AllowedJobKinds []string `json:"allowedJobKinds"`
}

// Health is what the last check of a host found. Its JSON form is the contract's ComputeHealth.
type Health struct {
	State     string     `json:"state"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	Detail    string     `json:"detail,omitempty"`
}

// Host is a compute host with its cards.
type Host struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Cards       []Card    `json:"cards"`
	Health      Health    `json:"health"`
	Rev         int       `json:"rev"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const hostCols = "id, name, description, cards, health, rev, created_at, updated_at"

func scanHost(row pgx.CollectableRow) (Host, error) {
	var h Host
	err := row.Scan(&h.ID, &h.Name, &h.Description, &h.Cards, &h.Health, &h.Rev, &h.CreatedAt, &h.UpdatedAt)
	return h, err
}

// List returns hosts by name.
func List(ctx context.Context, q storage.Querier) ([]Host, error) {
	rows, err := q.Query(ctx, "SELECT "+hostCols+" FROM compute_hosts ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list compute hosts: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanHost)
	if err != nil {
		return nil, fmt.Errorf("list compute hosts: %w", err)
	}
	return out, nil
}

// Get returns the host with this id or name, or not-found.
func Get(ctx context.Context, q storage.Querier, idOrName string) (Host, error) {
	return get(ctx, q, idOrName, "")
}

func get(ctx context.Context, q storage.Querier, idOrName, lock string) (Host, error) {
	rows, err := q.Query(ctx, "SELECT "+hostCols+" FROM compute_hosts WHERE id = $1 OR name = $1 "+lock, idOrName)
	if err != nil {
		return Host{}, fmt.Errorf("query compute host: %w", err)
	}
	h, err := pgx.CollectExactlyOneRow(rows, scanHost)
	if errors.Is(err, pgx.ErrNoRows) {
		return Host{}, problems.NotFound.New("no compute host %q", idOrName)
	}
	if err != nil {
		return Host{}, fmt.Errorf("read compute host: %w", err)
	}
	return h, nil
}

// CardEdit changes one card; nil fields stay as they are.
type CardEdit struct {
	Index           int
	MemoryCapGB     *float64
	AllowedJobKinds *[]string
}

// EditInput is the body of compute.edit.
type EditInput struct {
	Description *string
	Cards       []CardEdit
}

// Edit changes the host at revision rev. A memory cap must be above zero and at most the card's memory; job kinds
// must be known. Field problems answer validation-failed with one entry per field.
func Edit(ctx context.Context, tx pgx.Tx, idOrName string, rev int, in EditInput) (Host, []events.Draft, error) {
	h, err := get(ctx, tx, idOrName, "FOR UPDATE")
	if err != nil {
		return Host{}, nil, err
	}
	if err := commands.CheckRev(Kind, rev, h.Rev); err != nil {
		return Host{}, nil, err
	}
	var fields []problems.FieldError
	for i, e := range in.Cards {
		at := slices.IndexFunc(h.Cards, func(c Card) bool { return c.Index == e.Index })
		if at < 0 {
			fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/cards/%d/index", i),
				Message: fmt.Sprintf("host %s has no card %d", h.Name, e.Index)})
			continue
		}
		c := &h.Cards[at]
		if e.MemoryCapGB != nil {
			if *e.MemoryCapGB <= 0 || *e.MemoryCapGB > c.MemoryGB {
				fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/cards/%d/memoryCapGb", i),
					Message: fmt.Sprintf("must be above 0 and at most the card's %v GB", c.MemoryGB)})
			} else {
				c.MemoryCapGB = *e.MemoryCapGB
			}
		}
		if e.AllowedJobKinds != nil {
			for j, k := range *e.AllowedJobKinds {
				if !slices.Contains(JobKinds, k) {
					fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/cards/%d/allowedJobKinds/%d", i, j),
						Message: fmt.Sprintf("unknown job kind %q", k)})
				}
			}
			c.AllowedJobKinds = append([]string{}, *e.AllowedJobKinds...)
		}
	}
	if len(fields) > 0 {
		return Host{}, nil, problems.Validation(fields)
	}
	if in.Description != nil {
		h.Description = *in.Description
	}
	rows, err := tx.Query(ctx, `UPDATE compute_hosts SET description = $2, cards = $3, rev = rev + 1, updated_at = now()
		WHERE id = $1 RETURNING `+hostCols, h.ID, h.Description, h.Cards)
	if err != nil {
		return Host{}, nil, fmt.Errorf("update compute host: %w", err)
	}
	if h, err = pgx.CollectExactlyOneRow(rows, scanHost); err != nil {
		return Host{}, nil, fmt.Errorf("update compute host: %w", err)
	}
	return h, []events.Draft{hostEvent(h, "compute.edited")}, nil
}

// Compute events are instance-wide: no projectId.
func hostEvent(h Host, typ string) events.Draft {
	return events.Draft{
		Topic:   events.EntityTopic(Kind, h.ID),
		Type:    typ,
		Entity:  &events.EntityRef{Kind: Kind, ID: h.ID, Rev: h.Rev},
		Payload: map[string]any{"compute": h},
	}
}

// Seed creates the hosts of defaults.yaml that do not exist yet (by name); hosts already there are left as
// Settings edited them. It returns how many it created.
func Seed(ctx context.Context, pool *pgxpool.Pool, hosts []defaults.Host, actor auth.Actor) (int, error) {
	added := 0
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var drafts []events.Draft
		for _, dh := range hosts {
			cards := make([]Card, 0, len(dh.Cards))
			for _, c := range dh.Cards {
				cards = append(cards, Card{Index: c.Index, Name: c.Name, CardClass: c.CardClass, MemoryGB: c.MemoryGB,
					MemoryCapGB: c.MemoryCapGB, AllowedJobKinds: c.AllowedJobKinds})
			}
			rows, err := tx.Query(ctx, `INSERT INTO compute_hosts (id, name, description, cards) VALUES ($1, $2, $3, $4)
				ON CONFLICT (name) DO NOTHING RETURNING `+hostCols,
				"cmp_"+uuid.Must(uuid.NewV7()).String(), dh.Name, dh.Description, cards)
			if err != nil {
				return fmt.Errorf("seed compute host %s: %w", dh.Name, err)
			}
			h, err := pgx.CollectExactlyOneRow(rows, scanHost)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("seed compute host %s: %w", dh.Name, err)
			}
			added++
			drafts = append(drafts, hostEvent(h, "compute.created"))
		}
		return events.Append(ctx, tx, actor, nil, drafts)
	})
	if err != nil {
		return 0, err
	}
	return added, nil
}

// Slot is a card chosen for a job.
type Slot struct {
	Host Host
	Card Card
}

// ForJob picks the card a job of kind would run on: on the named host (id or name) when given, else on the first
// host by name; the first card by index that allows kind. A host that is known to be unreachable is skipped when
// no host was named.
func ForJob(ctx context.Context, q storage.Querier, host, kind string) (Slot, error) {
	var hosts []Host
	if host != "" {
		h, err := Get(ctx, q, host)
		if err != nil {
			return Slot{}, err
		}
		hosts = []Host{h}
	} else {
		all, err := List(ctx, q)
		if err != nil {
			return Slot{}, err
		}
		for _, h := range all {
			if h.Health.State != "unreachable" {
				hosts = append(hosts, h)
			}
		}
	}
	for _, h := range hosts {
		cards := slices.Clone(h.Cards)
		slices.SortFunc(cards, func(a, b Card) int { return a.Index - b.Index })
		for _, c := range cards {
			if slices.Contains(c.AllowedJobKinds, kind) {
				return Slot{Host: h, Card: c}, nil
			}
		}
	}
	if host != "" {
		return Slot{}, problems.Conflict.New("no card on host %q allows %s jobs; change its allowed job kinds (compute.edit)", host, kind)
	}
	return Slot{}, problems.Conflict.New("no compute card allows %s jobs; add the kind to a card (compute.edit)", kind)
}
