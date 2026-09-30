package runs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Roles of a model family descriptor (R41) that runs use. The step kind of each comes from the descriptor the
// family's runtime published; no code here knows a family by name.
const (
	RoleCalibrate = "calibrate"
	RoleTrain     = "train"
	RoleAverage   = "average"
)

// Family is a model family descriptor as runs need it.
type Family struct {
	Name      string            `json:"name"`
	VersionID string            `json:"versionId"`
	Roles     map[string]string `json:"roles"`
}

// Role returns the step kind that fills role, or family-unavailable.
func (f Family) Role(role string) (string, error) {
	if k := f.Roles[role]; k != "" {
		return k, nil
	}
	return "", problems.FamilyUnavailable.New("model family %s names no step kind for the %s role; its runtime's pack cannot %s", f.Name, role, role)
}

// RoleOf returns the role a step kind fills in f ("" when none).
func (f Family) RoleOf(kind string) string {
	for role, k := range f.Roles {
		if k == kind {
			return role
		}
	}
	return ""
}

// FamilyOf reads the family descriptor of base model version base: its payload's familyId names the collection
// model-family/<familyId>, whose newest frozen version a worker published. family-unavailable when none did.
func FamilyOf(ctx context.Context, q storage.Querier, base registry.Version) (Family, error) {
	var p struct {
		FamilyID string `json:"familyId"`
	}
	if err := json.Unmarshal(base.Payload, &p); err != nil {
		return Family{}, fmt.Errorf("decode base model %s: %w", base.ID, err)
	}
	if p.FamilyID == "" {
		return Family{}, problems.FamilyUnavailable.New("base model %s %s names no model family (familyId)", base.Name, base.Version)
	}
	v, err := registry.Latest(ctx, q, registry.KindModelFamily, "model-family/"+p.FamilyID)
	if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
		return Family{}, problems.FamilyUnavailable.New(
			"no worker has published model family %s (the family of %s); start a worker whose runtime carries it (runtimes.list, modelFamilies.list)",
			p.FamilyID, base.Name)
	}
	if err != nil {
		return Family{}, err
	}
	return familyFromVersion(v)
}

// familyVersion reads a family descriptor by its registry version id.
func familyVersion(ctx context.Context, q storage.Querier, id string) (Family, error) {
	v, err := registry.GetVersion(ctx, q, registry.KindModelFamily, id)
	if err != nil {
		return Family{}, err
	}
	return familyFromVersion(v)
}

func familyFromVersion(v registry.Version) (Family, error) {
	var d struct {
		Name  string            `json:"name"`
		Roles map[string]string `json:"roles"`
	}
	if err := json.Unmarshal(v.Payload, &d); err != nil {
		return Family{}, fmt.Errorf("decode model family %s: %w", v.ID, err)
	}
	if d.Roles == nil {
		d.Roles = map[string]string{}
	}
	return Family{Name: d.Name, VersionID: v.ID, Roles: d.Roles}, nil
}

// RoleKind returns the newest published version of step kind name (collection step-kind/<name>).
func RoleKind(ctx context.Context, q storage.Querier, name string) (pipelines.Kind, error) {
	list, err := registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindStepKind, Collection: "step-kind/" + name})
	if err != nil {
		return pipelines.Kind{}, err
	}
	for _, v := range list { // newest first
		if v.State == registry.StateDeprecated {
			continue
		}
		var k pipelines.Kind
		if err := json.Unmarshal(v.Payload, &k); err != nil {
			return pipelines.Kind{}, fmt.Errorf("step kind %s (%s): %w", name, v.ID, err)
		}
		if k.Name == "" {
			k.Name = name
		}
		k.VersionID = v.ID
		return k, nil
	}
	return pipelines.Kind{}, problems.FamilyUnavailable.New("no runtime publishes step kind %s; start a worker whose runtime carries it (stepKinds.list)", name)
}

// hasParam reports whether kind k's parameter schema has a property named name.
func hasParam(k pipelines.Kind, name string) bool {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(k.Params, &s); err != nil {
		return false
	}
	_, ok := s.Properties[name]
	return ok
}

// peakLRParams are the train-step parameter names runs.stage sets a stage's peak learning rate through, first match
// wins (a naming convention of train role kinds, docs/spec/08-resolutions.md R41; not a family name).
var peakLRParams = []string{"peak_lr", "learning_rate", "lr"}

// peakLRParam returns the parameter of train kind k that carries the peak learning rate.
func peakLRParam(k pipelines.Kind) (string, error) {
	for _, n := range peakLRParams {
		if hasParam(k, n) {
			return n, nil
		}
	}
	return "", problems.RecipeMismatch.New("the train step kind %s has no learning-rate parameter (%v); a stage's peak learning rate cannot be set",
		k.Ref(), peakLRParams)
}
