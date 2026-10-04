package pipelines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is a published step kind as the engine needs it: a step_kind registry version's payload (the contract's
// StepKindDescriptor plus name, runtime and runtimeVersionId) with its version id.
type Kind struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Runtime          string            `json:"runtime,omitempty"`
	RuntimeVersionID string            `json:"runtimeVersionId,omitempty"`
	Params           json.RawMessage   `json:"params"`
	Consumes         map[string]string `json:"consumes"`
	// OptionalInputs may be left unwired by a pipeline (a transcribe step's boost list); the step runs without them.
	OptionalInputs []string          `json:"optionalInputs,omitempty"`
	Produces       map[string]string `json:"produces"`
	// OptionalOutputs may be left unwritten by a successful step; no pipeline wires them into another step.
	OptionalOutputs []string        `json:"optionalOutputs,omitempty"`
	Resources       steps.Resources `json:"resources"`
	Secrets         []string        `json:"secrets,omitempty"`
	Help            string          `json:"help,omitempty"`
	// EstimateSeconds is an optional fixed wall-time estimate a kind may publish (a data step's typical time);
	// facades pass better ones per run (StartInput.Estimates).
	EstimateSeconds *float64 `json:"estimateSeconds,omitempty"`
	// Deprecation is set when the kind's pack deprecates this version (StepKindDescriptor.deprecation): plans warn,
	// and from After a pipeline file may not newly pin it (deprecation.go).
	Deprecation *Deprecation `json:"deprecation,omitempty"`

	VersionID string `json:"-"` // ver_ of the registry version
}

// Ref is the pinned name@version.
func (k Kind) Ref() string { return k.Name + "@" + k.Version }

// Kinds finds published step kinds.
type Kinds interface {
	// Lookup returns the newest registry version of step kind name at version; found is false when no runtime
	// published it.
	Lookup(ctx context.Context, q storage.Querier, name, version string) (k Kind, found bool, err error)
}

// RegistryKinds reads step kinds from the registry (kind step_kind, collection step-kind/<name>), where the worker
// protocol stores what workers publish at start.
type RegistryKinds struct{}

// Lookup implements Kinds.
func (RegistryKinds) Lookup(ctx context.Context, q storage.Querier, name, version string) (Kind, bool, error) {
	list, err := registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindStepKind, Collection: "step-kind/" + name})
	if err != nil {
		return Kind{}, false, err
	}
	for _, v := range list { // newest first
		if v.State == registry.StateDeprecated {
			continue
		}
		var k Kind
		if err := json.Unmarshal(v.Payload, &k); err != nil {
			return Kind{}, false, fmt.Errorf("step kind %s (%s): %w", name, v.ID, err)
		}
		if k.Version != version {
			continue
		}
		if k.Name == "" {
			k.Name = name
		}
		k.VersionID = v.ID
		return k, true, nil
	}
	return Kind{}, false, nil
}

// Publisher is implemented by Kinds that know what the registered workers publish now (RegistryKinds). Plan refuses
// a pinned kind@version that its runtime's workers no longer publish: the step would wait in the queue for ever.
type Publisher interface {
	// Published reports whether a registered worker of k's runtime publishes k now, and every version of k's name
	// that registered workers publish (sorted name@version). A runtime with no registered worker is not judged (ok
	// is true): nothing has said what it publishes, and its step waits for a worker like any other.
	Published(ctx context.Context, q storage.Querier, k Kind) (ok bool, versions []string, err error)
}

// Published implements Publisher from the workers table: a row is the latest registration of one runtime on one
// host, and its step_kinds are what that registration published (a version a re-registering runtime dropped is gone
// from it).
func (RegistryKinds) Published(ctx context.Context, q storage.Querier, k Kind) (bool, []string, error) {
	ref := k.Ref()
	var (
		workers, publishing int
		versions            []string
	)
	err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE runtime_name = $1),
			count(*) FILTER (WHERE runtime_name = $1 AND $2 = ANY(step_kinds)),
			coalesce((SELECT array_agg(DISTINCT r ORDER BY r) FROM workers, unnest(step_kinds) r WHERE split_part(r, '@', 1) = $3), '{}')
		FROM workers`, k.Runtime, ref, k.Name).Scan(&workers, &publishing, &versions)
	if err != nil {
		return false, nil, fmt.Errorf("read what workers publish of %s: %w", ref, err)
	}
	return k.Runtime == "" || workers == 0 || publishing > 0, versions, nil
}

// LiveWindow is how recently a worker must have been seen to count as alive: a waiting worker long-polls its claim
// (refreshing last_seen_at) at least every 30 s, and a working one heartbeats its lease.
const LiveWindow = 2 * time.Minute

// Liveness is implemented by Kinds that know whether a worker that publishes a kind is alive now (RegistryKinds).
// Plan skips an optional step whose kind no live worker publishes, instead of queueing a step nobody leases.
type Liveness interface {
	// Live reports whether a worker of k's runtime that publishes k was seen within LiveWindow. A runtime with no
	// registered worker is not judged (true), as in Published.
	Live(ctx context.Context, q storage.Querier, k Kind) (bool, error)
}

// Live implements Liveness from the workers table.
func (RegistryKinds) Live(ctx context.Context, q storage.Querier, k Kind) (bool, error) {
	var workers, alive int
	err := q.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE $2 = ANY(step_kinds) AND last_seen_at > now() - $3::interval)
		FROM workers WHERE runtime_name = $1`, k.Runtime, k.Ref(), LiveWindow).Scan(&workers, &alive)
	if err != nil {
		return false, fmt.Errorf("read the live workers of %s: %w", k.Ref(), err)
	}
	return k.Runtime == "" || workers == 0 || alive > 0, nil
}

// runtimeOf returns the runtime version id (runtimeVersionId) of the pinned step kind registry version, part of a
// step's input hash; "" when the step pins no version or the version names no runtime.
func runtimeOf(ctx context.Context, q storage.Querier, stepKindVersionID string) (string, error) {
	if stepKindVersionID == "" {
		return "", nil
	}
	var id string
	err := q.QueryRow(ctx, `SELECT coalesce(payload->>'runtimeVersionId', '') FROM registry_versions WHERE id = $1`,
		stepKindVersionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the runtime of step kind version %s: %w", stepKindVersionID, err)
	}
	return id, nil
}
