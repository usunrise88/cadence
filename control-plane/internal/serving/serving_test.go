package serving

import (
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

func TestRefusal(t *testing.T) {
	staging := targets.Target{Name: "staging", Kind: targets.KindStaging, State: targets.StateActive, Config: targets.Config{
		Endpoint: "http://triton:8000", Serves: []targets.Serves{{Family: "fam", Formats: []string{"step-graph"}, Profiles: []string{"80ms", "160ms"}}}}}
	archived := staging
	archived.State = targets.StateArchived
	delivery := targets.Target{Name: "era", Kind: targets.KindDelivery, State: targets.StateActive}
	bare := staging
	bare.Serves = nil
	tests := []struct {
		name string
		t    targets.Target
		dep  Deployable
		want string
	}{
		{"serves it", staging, Deployable{Family: "fam", Format: "step-graph", Profile: "160ms"}, ""},
		{"a deployable that names nothing passes", staging, Deployable{}, ""},
		{"archived", archived, Deployable{}, "archived"},
		{"delivery targets are never reached", delivery, Deployable{}, "never reaches it"},
		{"another family", staging, Deployable{Family: "other"}, "does not serve model family other (it serves fam)"},
		{"another format", staging, Deployable{Family: "fam", Format: "onnx"}, "in step-graph, not onnx"},
		{"another profile", staging, Deployable{Family: "fam", Profile: "1120ms"}, "at 80ms, 160ms, not at 1120ms"},
		{"serves nothing yet", bare, Deployable{Family: "fam"}, "nothing yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := refusal(tt.t, tt.dep)
			if tt.want == "" && got != "" || !strings.Contains(got, tt.want) {
				t.Fatalf("refusal = %q, want %q", got, tt.want)
			}
		})
	}
}

// The embedded defaults name the paths of the seeded staging target's server kind, so its health is checked.
func TestDefaultsNameTheStagingServer(t *testing.T) {
	d := defaults.Get()
	st := d.Serving.StagingTarget.Value
	paths, ok := d.Serving.Servers.Value[st.Server.Kind]
	if !ok || paths.Health == "" || paths.Index == "" || !strings.Contains(paths.Unload, "{model}") {
		t.Fatalf("serving.servers[%s] = %+v", st.Server.Kind, paths)
	}
	if d.Serving.DefaultTarget.Value != st.Name || len(st.Serves) == 0 || FallbackMB(d) != 9*1024 {
		t.Fatalf("serving defaults: default target %q, staging %+v", d.Serving.DefaultTarget.Value, st)
	}
}
