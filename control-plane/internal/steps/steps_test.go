package steps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
)

const h64 = "b3:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSpecValidate(t *testing.T) {
	ok := Spec{StepID: "pls_1", PipelineRunID: "plr_1", Kind: "echo", KindVersion: "1", Attempt: 1,
		Inputs: map[string]ArtifactRef{"text": {Hash: h64, Type: "text"}}}
	for _, tc := range []struct {
		name string
		mut  func(*Spec)
		want string
	}{
		{"valid", func(*Spec) {}, ""},
		{"no step", func(s *Spec) { s.StepID = "" }, "stepId"},
		{"no version", func(s *Spec) { s.KindVersion = "" }, "kindVersion"},
		{"attempt zero", func(s *Spec) { s.Attempt = 0 }, "attempt"},
		{"two cards", func(s *Spec) { s.Resources.GPUs = 2 }, "one card"},
		{"bad hash", func(s *Spec) { s.Inputs = map[string]ArtifactRef{"text": {Hash: "sha256:x"}} }, "not an artifact hash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ok
			tc.mut(&s)
			err := s.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if got := ok.KindRef(); got != "echo@1" {
		t.Fatalf("KindRef = %q", got)
	}
}

func TestSpecJSONMatchesContract(t *testing.T) {
	b, err := json.Marshal(Spec{StepID: "pls_1", PipelineRunID: "plr_1", Kind: "echo", KindVersion: "1", Attempt: 1,
		Params: json.RawMessage(`{}`), Overrides: Overrides{BatchScale: OOMBatchScale}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"stepId"`, `"pipelineRunId"`, `"kindVersion"`, `"batchScale":0.75`, `"attempt":1`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("%s missing from %s", key, b)
		}
	}
}

func TestHooks(t *testing.T) {
	var h Hooks
	var order []string
	h.On("checkpoint", func(_ context.Context, _ pgx.Tx, o Output) ([]events.Draft, error) {
		order = append(order, "a:"+o.Name)
		return []events.Draft{{Topic: "run.r.status"}}, nil
	})
	h.On("checkpoint", func(_ context.Context, _ pgx.Tx, o Output) ([]events.Draft, error) {
		order = append(order, "b:"+o.Name)
		return nil, nil
	})
	h.On("dataset", func(context.Context, pgx.Tx, Output) ([]events.Draft, error) { return nil, errors.New("boom") })

	ev, err := h.Run(context.Background(), nil, Output{Name: "best", Artifact: ArtifactRef{Type: "checkpoint"}})
	if err != nil || len(ev) != 1 || strings.Join(order, ",") != "a:best,b:best" {
		t.Fatalf("ev=%v err=%v order=%v", ev, err, order)
	}
	if _, err := h.Run(context.Background(), nil, Output{Name: "d", Artifact: ArtifactRef{Type: "dataset"}}); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if ev, err := h.Run(context.Background(), nil, Output{Artifact: ArtifactRef{Type: "text"}}); err != nil || ev != nil {
		t.Fatalf("no hooks: ev=%v err=%v", ev, err)
	}
	if got := strings.Join(h.Types(), ","); got != "checkpoint,dataset" {
		t.Fatalf("Types = %s", got)
	}
	if _, err := (NoLeases{}).Await(context.Background(), "job_1"); !errors.Is(err, ErrNoWorkerProtocol) {
		t.Fatal(err)
	}
}
