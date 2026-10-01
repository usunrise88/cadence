package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Registration is what a worker publishes at start (the contract's WorkerRegistration). Descriptors stay raw: they
// are stored as registry payloads as published.
type Registration struct {
	Host          string                     `json:"host"`
	Instance      string                     `json:"instance,omitempty"`
	Runtime       json.RawMessage            `json:"runtime"`
	StepKinds     map[string]json.RawMessage `json:"stepKinds"`
	ModelFamilies []json.RawMessage          `json:"modelFamilies,omitempty"`
}

// runtimeHead is the part of a RuntimeDescriptor the protocol reads.
type runtimeHead struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`
}

// StepKind is the part of a StepKindDescriptor the protocol reads.
type StepKind struct {
	Version string          `json:"version"`
	Params  json.RawMessage `json:"params"`
	Neutral bool            `json:"neutral,omitempty"`
	Secrets []string        `json:"secrets,omitempty"`
}

// Worker is one registered worker (the contract's Worker).
type Worker struct {
	ID               string          `json:"id"`
	Host             string          `json:"host"`
	HostID           string          `json:"hostId"`
	Instance         string          `json:"instance,omitempty"`
	Runtime          json.RawMessage `json:"runtime"`
	RuntimeVersionID string          `json:"runtimeVersionId"`
	StepKinds        []string        `json:"stepKinds"`
	State            string          `json:"state"`
	LastSeenAt       time.Time       `json:"lastSeenAt"`
}

var (
	stepKindName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,61}[a-z0-9]$`)
	familyName   = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,61}[a-z0-9]$`)
)

// xCadenceKeys must all be present in every parameter's x-cadence (non-negotiable 5).
var xCadenceKeys = []string{"default", "description", "source", "range"}

// MissingXCadence lists the parameters of a JSON Schema whose x-cadence metadata is absent or incomplete, sorted.
func MissingXCadence(schema json.RawMessage) ([]string, error) {
	var doc struct {
		Type       string                                `json:"type"`
		Properties map[string]map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil, fmt.Errorf("params is not a JSON Schema object: %w", err)
	}
	var bad []string
	for name, prop := range doc.Properties {
		var meta map[string]json.RawMessage
		if raw, ok := prop["x-cadence"]; !ok || json.Unmarshal(raw, &meta) != nil || meta == nil {
			bad = append(bad, name)
			continue
		}
		for _, k := range xCadenceKeys {
			if _, ok := meta[k]; !ok {
				bad = append(bad, name)
				break
			}
		}
	}
	sort.Strings(bad)
	return bad, nil
}

// SchemaHash is the sha256 (hex) of the canonical JSON of a parameter schema's identity: neutral kinds of several
// runtimes must agree on it, and a kind@version keeps it for good. Wording (description, title, x-cadence
// description and source) is not identity, nor is a default that comes from defaults.yaml through x-cadence
// defaultRef (R11: a changed default reaches every pipeline without a new kind version); names, types, constraints,
// literal defaults and ranges are.
func SchemaHash(schema json.RawMessage) (string, error) {
	var doc any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return "", fmt.Errorf("parameter schema: %w", err)
	}
	b, err := json.Marshal(schemaIdentity(doc))
	if err != nil {
		return "", err
	}
	c, err := registry.Canonical(b)
	if err != nil {
		return "", err
	}
	return registry.Fingerprint(c), nil
}

// schemaIdentity drops what SchemaHash leaves out, recursively.
func schemaIdentity(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		ref := ""
		if xc, ok := t["x-cadence"].(map[string]any); ok {
			ref, _ = xc["defaultRef"].(string)
		}
		for k, val := range t {
			switch k {
			case "description", "title", "examples":
				continue
			case "default":
				if ref != "" {
					continue
				}
			case "x-cadence":
				xc, ok := val.(map[string]any)
				if !ok {
					continue
				}
				keep := map[string]any{}
				if ref != "" {
					keep["defaultRef"] = ref
				} else {
					for _, kk := range []string{"default", "range"} {
						if x, ok := xc[kk]; ok {
							keep[kk] = x
						}
					}
				}
				out[k] = keep
				continue
			}
			out[k] = schemaIdentity(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = schemaIdentity(x)
		}
		return out
	default:
		return v
	}
}

// Register records a worker's publication: its runtime, model families and step kinds as frozen registry versions
// (content already registered is reused, so a restart registers nothing new) and the worker row. A step kind whose
// parameters lack complete x-cadence metadata is refused, as is a framework kind that another runtime already
// publishes under the same name and version (R40) or a neutral kind whose schema differs from another runtime's.
func (s *Service) Register(ctx context.Context, tx pgx.Tx, c Caller, in Registration) (Worker, []events.Draft, error) {
	if !c.allows(in.Host) {
		return Worker{}, nil, problems.Forbidden.New("this worker token belongs to host %q, not %q", c.Host, in.Host)
	}
	host, err := compute.Get(ctx, tx, in.Host)
	if err != nil {
		return Worker{}, nil, err
	}
	var rt runtimeHead
	if err := json.Unmarshal(in.Runtime, &rt); err != nil || rt.Name == "" || rt.Version == "" {
		return Worker{}, nil, problems.Validation([]problems.FieldError{{Path: "/runtime", Message: "name and version are required"}})
	}
	kinds, fields := parseKinds(in.StepKinds)
	for i, raw := range in.ModelFamilies {
		var f struct{ Name string }
		if json.Unmarshal(raw, &f) != nil || !familyName.MatchString(f.Name) {
			fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/modelFamilies/%d/name", i), Message: "a lower-case family name is required"})
		}
	}
	if len(fields) > 0 {
		return Worker{}, nil, problems.Validation(fields)
	}
	now := s.now()
	var drafts []events.Draft
	register := func(kind, name string, payload []byte) (registry.Version, error) {
		v, _, d, err := registry.Register(ctx, tx, registry.RegisterInput{
			Kind: kind, Name: name, Payload: payload, Actor: c.Actor, Freeze: true,
			Description: fmt.Sprintf("Published by the %s runtime", rt.Name), Tags: []string{"runtime:" + rt.Name},
		}, now)
		drafts = append(drafts, d...)
		return v, err
	}
	rtv, err := register(registry.KindRuntime, "runtime/"+rt.Name, in.Runtime)
	if err != nil {
		return Worker{}, nil, err
	}
	for _, raw := range in.ModelFamilies {
		var f struct{ Name string }
		_ = json.Unmarshal(raw, &f)
		if _, err := register(registry.KindModelFamily, "model-family/"+f.Name, raw); err != nil {
			return Worker{}, nil, err
		}
	}
	refs := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if err := checkKindConflict(ctx, tx, rt.Name, k); err != nil {
			return Worker{}, nil, err
		}
		payload, err := stepKindPayload(k, rt.Name, rtv.ID)
		if err != nil {
			return Worker{}, nil, err
		}
		if _, err := register(registry.KindStepKind, "step-kind/"+k.name, payload); err != nil {
			return Worker{}, nil, err
		}
		refs = append(refs, k.name+"@"+k.desc.Version)
	}
	sort.Strings(refs)
	w, reaped, err := s.upsertWorker(ctx, tx, c, host, in, rt.Name, rtv.ID, refs, now)
	if err != nil {
		return Worker{}, nil, err
	}
	drafts = append(drafts, reaped...)
	drafts = append(drafts, events.Draft{Topic: "compute." + host.ID, Type: EventRegistered, Payload: map[string]any{"worker": w}})
	return w, drafts, nil
}

type publishedKind struct {
	name       string
	desc       StepKind
	raw        json.RawMessage
	schemaHash string
}

func parseKinds(in map[string]json.RawMessage) ([]publishedKind, []problems.FieldError) {
	var (
		out    []publishedKind
		fields []problems.FieldError
	)
	names := make([]string, 0, len(in))
	for n := range in {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		at := "/stepKinds/" + name
		if !stepKindName.MatchString(name) {
			fields = append(fields, problems.FieldError{Path: at, Message: "a step kind name is lower_snake_case"})
			continue
		}
		var k StepKind
		if err := json.Unmarshal(in[name], &k); err != nil || k.Version == "" || len(k.Params) == 0 {
			fields = append(fields, problems.FieldError{Path: at, Message: "version and params are required"})
			continue
		}
		bad, err := MissingXCadence(k.Params)
		if err != nil {
			fields = append(fields, problems.FieldError{Path: at + "/params", Message: err.Error()})
			continue
		}
		for _, p := range bad {
			fields = append(fields, problems.FieldError{Path: at + "/params/properties/" + p + "/x-cadence",
				Message: "every parameter needs x-cadence with default, description, source and range"})
		}
		hash, err := SchemaHash(k.Params)
		if err != nil {
			fields = append(fields, problems.FieldError{Path: at + "/params", Message: err.Error()})
			continue
		}
		out = append(out, publishedKind{name: name, desc: k, raw: in[name], schemaHash: hash})
	}
	return out, fields
}

// stepKindPayload is the registry payload of a step kind: its descriptor plus name, runtime, runtime version and
// schema hash.
func stepKindPayload(k publishedKind, runtime, runtimeVersionID string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(k.raw, &doc); err != nil {
		return nil, fmt.Errorf("step kind %s: %w", k.name, err)
	}
	doc["name"], doc["runtime"], doc["runtimeVersionId"], doc["schemaHash"] = k.name, runtime, runtimeVersionID, k.schemaHash
	return json.Marshal(doc)
}

// checkKindConflict enforces R40 and the neutral-kind rule against what other runtimes published.
func checkKindConflict(ctx context.Context, q storage.Querier, runtime string, k publishedKind) error {
	// The other runtime's hash is recomputed from its stored schema, so a change of SchemaHash's rule never makes two
	// equal schemas disagree.
	rows, err := q.Query(ctx, `SELECT v.payload->>'runtime', coalesce((v.payload->>'neutral')::boolean, false), v.payload->'params'
		FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.kind = 'step_kind' AND c.name = $1 AND v.payload->>'version' = $2 AND v.payload->>'runtime' <> $3`,
		"step-kind/"+k.name, k.desc.Version, runtime)
	if err != nil {
		return fmt.Errorf("read published step kinds: %w", err)
	}
	type other struct {
		runtime, hash string
		neutral       bool
	}
	others, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (other, error) {
		var (
			o      other
			params []byte
		)
		if err := row.Scan(&o.runtime, &o.neutral, &params); err != nil {
			return o, err
		}
		h, err := SchemaHash(params)
		o.hash = h
		return o, err
	})
	if err != nil {
		return fmt.Errorf("read published step kinds: %w", err)
	}
	for _, o := range others {
		switch {
		case !k.desc.Neutral || !o.neutral:
			return problems.StepKindConflict.New("step kind %s@%s is already published by the %s runtime; a framework step kind lives in one runtime (R40) — rename it or bump its version",
				k.name, k.desc.Version, o.runtime)
		case o.hash != k.schemaHash:
			return problems.StepKindConflict.New("neutral step kind %s@%s has a different parameter schema in the %s runtime; bump its version",
				k.name, k.desc.Version, o.runtime)
		}
	}
	return nil
}

// upsertWorker records the worker (one per runtime and host). A registration from a new instance means the old
// process is gone: its leases are reaped at once instead of after three missed beats.
func (s *Service) upsertWorker(ctx context.Context, tx pgx.Tx, c Caller, host compute.Host, in Registration, runtime,
	runtimeVersionID string, refs []string, now time.Time) (Worker, []events.Draft, error) {
	var (
		id, prevInstance string
		found            bool
	)
	err := tx.QueryRow(ctx, `SELECT id, instance FROM workers WHERE host_id = $1 AND runtime_name = $2 FOR UPDATE`,
		host.ID, runtime).Scan(&id, &prevInstance)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		id = "wrk_" + uuid.Must(uuid.NewV7()).String()
	case err != nil:
		return Worker{}, nil, fmt.Errorf("read worker: %w", err)
	default:
		found = true
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workers (id, host_id, instance, runtime_name, runtime_version_id, runtime, step_kinds,
			credential_id, registered_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $9)
		ON CONFLICT (id) DO UPDATE SET instance = excluded.instance, runtime_version_id = excluded.runtime_version_id,
			runtime = excluded.runtime, step_kinds = excluded.step_kinds, credential_id = excluded.credential_id,
			registered_at = excluded.registered_at, last_seen_at = excluded.last_seen_at`,
		id, host.ID, in.Instance, runtime, runtimeVersionID, in.Runtime, refs, c.CredentialID, now); err != nil {
		return Worker{}, nil, fmt.Errorf("record worker: %w", err)
	}
	var drafts []events.Draft
	if found && prevInstance != in.Instance {
		d, err := s.reapWhere(ctx, tx, "worker_id = $1", id)
		if err != nil {
			return Worker{}, nil, err
		}
		drafts = d
	}
	return Worker{ID: id, Host: host.Name, HostID: host.ID, Instance: in.Instance, Runtime: in.Runtime,
		RuntimeVersionID: runtimeVersionID, StepKinds: refs, State: "online", LastSeenAt: now}, drafts, nil
}

// ListWorkers returns the workers of a runtime version, newest registration first (runtimes.get).
func (s *Service) ListWorkers(ctx context.Context, q storage.Querier, runtimeVersionIDs []string) (map[string][]Worker, error) {
	rows, err := q.Query(ctx, `SELECT w.id, h.name, w.host_id, w.instance, w.runtime, w.runtime_version_id, w.step_kinds, w.last_seen_at
		FROM workers w JOIN compute_hosts h ON h.id = w.host_id WHERE w.runtime_version_id = ANY($1)
		ORDER BY w.registered_at DESC`, runtimeVersionIDs)
	if err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Worker, error) {
		var w Worker
		err := row.Scan(&w.ID, &w.Host, &w.HostID, &w.Instance, &w.Runtime, &w.RuntimeVersionID, &w.StepKinds, &w.LastSeenAt)
		return w, err
	})
	if err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	out := map[string][]Worker{}
	for _, w := range list {
		w.State = "offline"
		if s.now().Sub(w.LastSeenAt) < MaxClaimWait+s.lostAfter() {
			w.State = "online"
		}
		out[w.RuntimeVersionID] = append(out[w.RuntimeVersionID], w)
	}
	return out, nil
}

// worker reads the worker id for c, or not-found / forbidden.
func (s *Service) worker(ctx context.Context, q storage.Querier, c Caller, id string) (Worker, error) {
	var w Worker
	err := q.QueryRow(ctx, `SELECT w.id, h.name, w.host_id, w.instance, w.runtime, w.runtime_version_id, w.step_kinds, w.last_seen_at
		FROM workers w JOIN compute_hosts h ON h.id = w.host_id WHERE w.id = $1`, id).
		Scan(&w.ID, &w.Host, &w.HostID, &w.Instance, &w.Runtime, &w.RuntimeVersionID, &w.StepKinds, &w.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, problems.NotFound.New("no worker %q; register first (workerRegistrations.new)", id)
	}
	if err != nil {
		return Worker{}, fmt.Errorf("read worker: %w", err)
	}
	if !c.allows(w.Host) {
		return Worker{}, problems.Forbidden.New("worker %s runs on host %q; this token belongs to %q", id, w.Host, c.Host)
	}
	w.State = "online"
	return w, nil
}
