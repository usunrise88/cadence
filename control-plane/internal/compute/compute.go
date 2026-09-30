// Package compute holds the compute entity: hosts and their cards with the memory cap per card, the job kinds each
// card accepts and the host's health (docs/spec/02-domain-projects-registry.md, entity Compute). Settings edits it
// now; the queue schedules against it from phase 2. Hosts are seeded from defaults.yaml at first start.
package compute

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
var JobKinds = []string{"training", "eval", "shadow", "export", "data"}

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
	// Windows are the card's availability windows per job kind (R19); none means always open.
	Windows Windows `json:"windows,omitempty"`
	// Telemetry is the card's last report from a worker; filled on read (WithTelemetry), never stored in cards.
	Telemetry *Telemetry `json:"telemetry,omitempty"`
}

// Telemetry is what a worker last reported about a card (the contract's CardTelemetryReport).
type Telemetry struct {
	Index         int       `json:"index"`
	Name          string    `json:"name,omitempty"`
	MemoryTotalMB *int      `json:"memoryTotalMb,omitempty"`
	MemoryUsedMB  *int      `json:"memoryUsedMb,omitempty"`
	Utilization   *float64  `json:"utilization,omitempty"`
	TemperatureC  *float64  `json:"temperatureC,omitempty"`
	PowerW        *float64  `json:"powerW,omitempty"`
	ReportedAt    time.Time `json:"reportedAt"`
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
	Name            *string
	CardClass       *string
	MemoryGB        *float64
	MemoryCapGB     *float64
	AllowedJobKinds *[]string
	Windows         *Windows // replaces the card's windows; an empty map clears them
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
		if e.Name != nil {
			c.Name = *e.Name
		}
		if e.CardClass != nil {
			c.CardClass = *e.CardClass
		}
		if e.MemoryGB != nil {
			if *e.MemoryGB <= 0 {
				fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/cards/%d/memoryGb", i), Message: "must be above 0"})
			} else {
				c.MemoryGB = *e.MemoryGB
			}
		}
		capGB := c.MemoryCapGB
		if e.MemoryCapGB != nil {
			capGB = *e.MemoryCapGB
		}
		if capGB <= 0 || capGB > c.MemoryGB {
			fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/cards/%d/memoryCapGb", i),
				Message: fmt.Sprintf("must be above 0 and at most the card's %v GB", c.MemoryGB)})
		} else {
			c.MemoryCapGB = capGB
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
		if e.Windows != nil {
			bad := ValidateWindows(*e.Windows)
			for _, at := range slices.Sorted(maps.Keys(bad)) {
				fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/cards/%d/windows%s", i, at), Message: bad[at]})
			}
			c.Windows = *e.Windows
			if len(c.Windows) == 0 {
				c.Windows = nil
			}
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

// WithTelemetry fills each card's last telemetry from the card slots the worker protocol keeps.
func WithTelemetry(ctx context.Context, q storage.Querier, hosts []Host) error {
	if len(hosts) == 0 {
		return nil
	}
	ids := make([]string, 0, len(hosts))
	for _, h := range hosts {
		ids = append(ids, h.ID)
	}
	rows, err := q.Query(ctx, `SELECT host_id, card_index, telemetry, reported_at FROM card_slots
		WHERE host_id = ANY($1) AND telemetry IS NOT NULL AND reported_at IS NOT NULL`, ids)
	if err != nil {
		return fmt.Errorf("read card telemetry: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			hostID string
			index  int
			t      Telemetry
			at     time.Time
		)
		if err := rows.Scan(&hostID, &index, &t, &at); err != nil {
			return fmt.Errorf("read card telemetry: %w", err)
		}
		t.Index, t.ReportedAt = index, at
		for hi := range hosts {
			if hosts[hi].ID != hostID {
				continue
			}
			for ci := range hosts[hi].Cards {
				if hosts[hi].Cards[ci].Index == index {
					tc := t
					hosts[hi].Cards[ci].Telemetry = &tc
				}
			}
		}
	}
	return rows.Err()
}

// SetHealth records what the last worker report says about a host; it emits compute.health on compute.{id} only
// when the state changes, so heartbeats do not flood the stream. The host's revision does not move: health is not
// a setting.
func SetHealth(ctx context.Context, tx pgx.Tx, hostID string, h Health) ([]events.Draft, error) {
	var prev Health
	if err := tx.QueryRow(ctx, "SELECT health FROM compute_hosts WHERE id = $1 FOR UPDATE", hostID).Scan(&prev); err != nil {
		return nil, fmt.Errorf("read compute health: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE compute_hosts SET health = $2 WHERE id = $1", hostID, h); err != nil {
		return nil, fmt.Errorf("update compute health: %w", err)
	}
	if prev.State == h.State && prev.Detail == h.Detail {
		return nil, nil
	}
	return []events.Draft{{Topic: "compute." + hostID, Type: "compute.health", Payload: map[string]any{"hostId": hostID, "health": h}}}, nil
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
