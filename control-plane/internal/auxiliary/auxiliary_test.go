package auxiliary

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/templates"
)

const weights = `{"roles":["pseudolabel"],"licence":"Apache-2.0","outputsCommercialUse":true,"languages":["*"],
	"hfRepo":"org/model","revision":"abc"}`

func TestParse(t *testing.T) {
	tests := []struct {
		name, payload, want string // want: "" = valid, else a fragment of the problem
	}{
		{"weights", weights, ""},
		{"service", `{"roles":["pseudolabel"],"licence":"x","outputsCommercialUse":true,"languages":["sr"],
			"service":{"kind":"grpc-asr","endpoint":"host:50051","protocol":"p.v1"}}`, ""},
		{"unknown field", `{"roles":["lid"],"licence":"x","outputsCommercialUse":true,"languages":["*"],"hfRepo":"a","revision":"b","extra":1}`, "does not parse"},
		{"no roles", `{"roles":[],"licence":"x","outputsCommercialUse":true,"languages":["*"],"hfRepo":"a","revision":"b"}`, "roles is empty"},
		{"bad role", `{"roles":["judge"],"licence":"x","outputsCommercialUse":true,"languages":["*"],"hfRepo":"a","revision":"b"}`, `role "judge"`},
		{"no verdict", `{"roles":["lid"],"licence":"x","languages":["*"],"hfRepo":"a","revision":"b"}`, "outputsCommercialUse is missing"},
		{"unpinned", `{"roles":["lid"],"licence":"x","outputsCommercialUse":true,"languages":["*"],"hfRepo":"a"}`, "pinned revision"},
		{"both", `{"roles":["lid"],"licence":"x","outputsCommercialUse":true,"languages":["*"],"hfRepo":"a","revision":"b",
			"service":{"kind":"k","endpoint":"h:1","protocol":"p"}}`, "not both"},
		{"neither", `{"roles":["lid"],"licence":"x","outputsCommercialUse":true,"languages":["*"]}`, "weights (hfRepo, revision) or a service"},
		{"bad endpoint", `{"roles":["lid"],"licence":"x","outputsCommercialUse":true,"languages":["*"],
			"service":{"kind":"k","endpoint":"nohost","protocol":"p"}}`, "not host:port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(json.RawMessage(tt.payload))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			pe, ok := problems.As(err)
			if !ok || pe.Type != problems.ValidationFailed || !strings.Contains(pe.Detail, tt.want) {
				t.Fatalf("got %v, want validation-failed with %q", err, tt.want)
			}
		})
	}
}

func TestSeededAuxiliariesParseAndAreAdoptable(t *testing.T) {
	inputs, err := registry.BundledInputs(templates.FS)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]Payload{}
	for _, in := range inputs {
		if in.Kind != Kind {
			continue
		}
		p, err := Parse(in.Payload)
		if err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
		if err := CheckAdoption(registry.Version{Kind: Kind, Name: in.Name, Payload: in.Payload}); err != nil {
			t.Errorf("%s: %v", in.Name, err)
		}
		seen[in.Name] = p
	}
	// Only the OK rows of the licence table (docs/review/2026-10-03-phase-4-plan.md, R26).
	for name, role := range map[string]string{
		"auxiliary/whisper-large-v3": RolePseudolabel, "auxiliary/whisper-he-ivrit": RolePseudolabel,
		"auxiliary/oasis": RolePseudolabel, "auxiliary/lid-voxlingua107": RoleLID, "auxiliary/omniasr-ctc-1b": RoleAlign,
	} {
		if p, ok := seen[name]; !ok || !p.Has(role) || len(p.Conditions) == 0 || p.CheckedAt == "" {
			t.Errorf("%s: %+v", name, p)
		}
	}
	if len(seen) != 5 {
		t.Errorf("seeded %d auxiliaries", len(seen))
	}
	if s := seen["auxiliary/oasis"].Service; s == nil || s.Endpoint == "" || s.TokenSecret == "" {
		t.Errorf("oasis service %+v", s)
	}
}

func TestCheckAdoptionRefusesForbiddenOutputs(t *testing.T) {
	nc := strings.Replace(weights, `"outputsCommercialUse":true`, `"outputsCommercialUse":false`, 1)
	err := CheckAdoption(registry.Version{Kind: Kind, Name: "auxiliary/nc", Version: "v", Payload: json.RawMessage(nc)})
	if pe, ok := problems.As(err); !ok || pe.Type != problems.AuxiliaryLicenceRefused {
		t.Fatalf("got %v", err)
	}
	if err := CheckAdoption(registry.Version{Kind: registry.KindDataset, Payload: json.RawMessage(nc)}); err != nil {
		t.Fatalf("other kinds pass: %v", err)
	}
}

type fakeProber map[string]error

func (f fakeProber) Probe(_ context.Context, endpoint string) error { return f[endpoint] }

func TestCheckServices(t *testing.T) {
	svc := json.RawMessage(`{"roles":["pseudolabel"],"licence":"x","outputsCommercialUse":true,"languages":["sr"],
		"service":{"kind":"grpc-asr","endpoint":"down:1","protocol":"p.v1"}}`)
	refs := map[string]steps.RegistryRef{
		"model":   {VersionID: "ver_w", Name: "auxiliary/w", Payload: json.RawMessage(weights)},
		"service": {VersionID: "ver_s", Name: "auxiliary/s", Version: "v1", Payload: svc},
	}
	if err := CheckServices(t.Context(), nil, "step", refs); err != nil {
		t.Fatalf("no prober checks nothing: %v", err)
	}
	if err := CheckServices(t.Context(), fakeProber{}, "step", refs); err != nil {
		t.Fatal(err)
	}
	err := CheckServices(t.Context(), fakeProber{"down:1": errors.New("connection refused")}, "members", refs)
	pe, ok := problems.As(err)
	if !ok || pe.Type != problems.AuxiliaryUnavailable || !strings.Contains(pe.Detail, "down:1") ||
		!strings.Contains(pe.Detail, "auxiliary/s") || !strings.Contains(pe.Detail, "never starts") {
		t.Fatalf("got %v", err)
	}
}

func TestDialProber(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := (DialProber{}).Probe(t.Context(), addr); err != nil {
		t.Fatalf("listening endpoint: %v", err)
	}
	_ = l.Close()
	if err := (DialProber{}).Probe(t.Context(), addr); err == nil {
		t.Fatal("a closed port answered")
	}
}
