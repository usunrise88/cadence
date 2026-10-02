package pipelines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
