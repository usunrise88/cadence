// Package auxiliary holds the registry kind auxiliary (auxiliary/<name>): LID classifiers, pseudo-label members and
// aligners (docs/review/2026-10-03-phase-4-plan.md "Decisions taken for phase 4" 5–7, R26). Its payload is data the
// worker pack that serves the role reads; the control plane checks only what gates it:
//
//   - adoption: an approval for everyone (preset rule auxiliary-adoption, kind=auxiliary) that refuses a licence
//     forbidding commercial use of the outputs (CheckAdoption);
//   - step parameters marked x-cadence.registryRef {kind: auxiliary, role}: the pipeline engine resolves them to the
//     version the project adopted, of that role (Resolve), and passes the payload to the worker in the step spec;
//   - a service payload ({service: {endpoint}}): the dry run checks that the endpoint answers (Prober). Cadence never
//     starts a service.
//
// Nothing here names a framework, a model or a service: those are payload values.
package auxiliary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the registry kind of auxiliary versions.
const Kind = registry.KindAuxiliary

// Roles an auxiliary fills (the contract's AuxiliaryRole).
const (
	RoleLID         = "lid"
	RolePseudolabel = "pseudolabel"
	RoleAlign       = "align"
)

var roles = []string{RoleLID, RolePseudolabel, RoleAlign}

// Service is a running service an auxiliary names (the contract's AuxiliaryService).
type Service struct {
	Kind        string `json:"kind"`
	Endpoint    string `json:"endpoint"`
	Protocol    string `json:"protocol"`
	TokenSecret string `json:"tokenSecret,omitempty"`
}

// Payload is an auxiliary version's payload (the contract's AuxiliaryPayload).
type Payload struct {
	Roles                []string `json:"roles"`
	Licence              string   `json:"licence"`
	OutputsCommercialUse *bool    `json:"outputsCommercialUse"`
	Conditions           []string `json:"conditions,omitempty"`
	Languages            []string `json:"languages"`
	HFRepo               string   `json:"hfRepo,omitempty"`
	Revision             string   `json:"revision,omitempty"`
	Service              *Service `json:"service,omitempty"`
	Engine               string   `json:"engine,omitempty"`
	Sources              []string `json:"sources,omitempty"`
	CheckedAt            string   `json:"checkedAt,omitempty"`
}

// Parse reads and checks a payload: known fields only, at least one known role, a licence, the outputs' commercial
// use stated, languages, and either weights ({hfRepo, revision}) or a service ({kind, endpoint, protocol}).
func Parse(raw json.RawMessage) (Payload, error) {
	var p Payload
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Payload{}, problems.ValidationFailed.New("the auxiliary payload does not parse: %v", err)
	}
	var bad []string
	if len(p.Roles) == 0 {
		bad = append(bad, "roles is empty")
	}
	for _, r := range p.Roles {
		if !slices.Contains(roles, r) {
			bad = append(bad, fmt.Sprintf("role %q is not one of %s", r, strings.Join(roles, ", ")))
		}
	}
	if strings.TrimSpace(p.Licence) == "" {
		bad = append(bad, "licence is empty")
	}
	if p.OutputsCommercialUse == nil {
		bad = append(bad, "outputsCommercialUse is missing (the licence check states it)")
	}
	if len(p.Languages) == 0 {
		bad = append(bad, "languages is empty (use [\"*\"] for any)")
	}
	weights := p.HFRepo != "" || p.Revision != ""
	switch {
	case weights && p.Service != nil:
		bad = append(bad, "name weights (hfRepo, revision) or a service, not both")
	case weights && (p.HFRepo == "" || p.Revision == ""):
		bad = append(bad, "weights need both hfRepo and a pinned revision")
	case !weights && p.Service == nil:
		bad = append(bad, "name weights (hfRepo, revision) or a service")
	case p.Service != nil && (p.Service.Kind == "" || p.Service.Endpoint == "" || p.Service.Protocol == ""):
		bad = append(bad, "a service needs kind, endpoint and protocol")
	case p.Service != nil:
		if _, _, err := net.SplitHostPort(p.Service.Endpoint); err != nil {
			bad = append(bad, fmt.Sprintf("service endpoint %q is not host:port", p.Service.Endpoint))
		}
	}
	if len(bad) > 0 {
		return Payload{}, problems.ValidationFailed.New("the auxiliary payload is invalid: %s", strings.Join(bad, "; "))
	}
	return p, nil
}

// Has reports whether the payload fills role.
func (p Payload) Has(role string) bool { return slices.Contains(p.Roles, role) }

// CheckAdoption refuses adopting an auxiliary version whose payload is invalid or whose licence forbids commercial
// use of its outputs (R26); versions of other kinds pass.
func CheckAdoption(v registry.Version) error {
	if v.Kind != Kind {
		return nil
	}
	p, err := Parse(v.Payload)
	if err != nil {
		return err
	}
	if !*p.OutputsCommercialUse {
		return problems.AuxiliaryLicenceRefused.New(
			"%s %s: its licence (%s) forbids commercial use of its outputs, so nothing it produces may reach a dataset (R26)",
			v.Name, v.Version, p.Licence)
	}
	return nil
}

// Resolve finds the version a step parameter names for a project: ref is a collection name (the newest version of
// it the project adopted), ver_… (which the project must have adopted) or @alias. Without a project (a pipeline
// file checked on save) the collection's newest frozen version stands in and adoption is not checked. The version
// must fill role when role is set.
func Resolve(ctx context.Context, q storage.Querier, projectID, role, ref string) (steps.RegistryRef, Payload, error) {
	var (
		v   registry.Version
		err error
	)
	switch {
	case projectID == "":
		v, err = registry.Resolve(ctx, q, "", Kind, ref)
	case strings.HasPrefix(ref, "@") || strings.HasPrefix(ref, "ver_"):
		if v, err = registry.Resolve(ctx, q, projectID, Kind, ref); err == nil {
			err = adopted(ctx, q, projectID, v)
		}
	default:
		v, err = newestAdopted(ctx, q, projectID, ref)
	}
	if err != nil {
		return steps.RegistryRef{}, Payload{}, err
	}
	p, err := Parse(v.Payload)
	if err != nil {
		return steps.RegistryRef{}, Payload{}, err
	}
	if role != "" && !p.Has(role) {
		return steps.RegistryRef{}, Payload{}, problems.ValidationFailed.New("%s fills %s, not %s",
			v.Name, strings.Join(p.Roles, ", "), role)
	}
	if !*p.OutputsCommercialUse {
		return steps.RegistryRef{}, Payload{}, problems.AuxiliaryLicenceRefused.New(
			"%s %s: its licence forbids commercial use of its outputs (R26)", v.Name, v.Version)
	}
	return steps.RegistryRef{VersionID: v.ID, Name: v.Name, Version: v.Version, Payload: v.Payload}, p, nil
}

func adopted(ctx context.Context, q storage.Querier, projectID string, v registry.Version) error {
	var ok bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM adoptions WHERE project_id = $1 AND version_id = $2)",
		projectID, v.ID).Scan(&ok); err != nil {
		return fmt.Errorf("check adoption: %w", err)
	}
	if !ok {
		return problems.NotAdopted.New("the project has not adopted %s %s; adopt it first (projects.adopt — an approval, R26), which writes it into data.lock", v.Name, v.Version)
	}
	return nil
}

func newestAdopted(ctx context.Context, q storage.Querier, projectID, name string) (registry.Version, error) {
	list, err := registry.ListAdoptions(ctx, q, projectID, Kind)
	if err != nil {
		return registry.Version{}, err
	}
	var best *registry.Version
	for i := range list {
		v := list[i].Version
		if v.Name == name && v.State == registry.StateFrozen && (best == nil || v.CreatedAt.After(best.CreatedAt)) {
			best = &v
		}
	}
	if best == nil {
		if _, err := registry.Latest(ctx, q, Kind, name); err != nil {
			return registry.Version{}, err
		}
		return registry.Version{}, problems.NotAdopted.New(
			"the project has not adopted %s; adopt a version of it first (projects.adopt — an approval, R26), which writes it into data.lock", name)
	}
	return *best, nil
}

// Prober checks that a service endpoint answers.
type Prober interface {
	Probe(ctx context.Context, endpoint string) error
}

// DialProber opens a TCP connection to the endpoint and closes it: the service listens. Whether it serves the
// protocol is the worker step's check (its health call); the dry run only saves a queued step from waiting on a
// service that is not running.
type DialProber struct {
	Timeout time.Duration // 3 s when zero
}

// Probe dials endpoint.
func (d DialProber) Probe(ctx context.Context, endpoint string) error {
	t := d.Timeout
	if t == 0 {
		t = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return err
	}
	return c.Close()
}

// CheckServices probes the service of every reference whose payload names one; the first that does not answer is
// an auxiliary-unavailable problem naming the step and the parameter.
func CheckServices(ctx context.Context, p Prober, step string, refs map[string]steps.RegistryRef) error {
	if p == nil {
		return nil
	}
	for _, param := range sortedKeys(refs) {
		ref := refs[param]
		pl, err := Parse(ref.Payload)
		if err != nil || pl.Service == nil {
			continue
		}
		if err := p.Probe(ctx, pl.Service.Endpoint); err != nil {
			return problems.AuxiliaryUnavailable.New(
				"step %s: %s (%s %s) does not answer at %s: %v. Cadence never starts it; start the service (its card says how) and run again",
				step, param, ref.Name, ref.Version, pl.Service.Endpoint, err)
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
