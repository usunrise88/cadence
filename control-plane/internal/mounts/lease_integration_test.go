//go:build integration

package mounts

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// A lease carries a mount's credentials only to a step that reads the mount: one it names, or one a step that
// produced its inputs named (back through their inputs) — not to every data step (audit M4).
func TestLeaseCredentialsFollowTheInputs(t *testing.T) {
	ctx := context.Background()
	pool, err := storage.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := storage.Migrate(ctx, pool, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	actor := `{"kind":"user","id":"usr_admin"}`
	for _, name := range []string{"calls", "other"} {
		exec(`INSERT INTO mounts (id, name, kind, root, endpoint, credentials, created_by)
			VALUES ($1, $2, 's3', 'bucket', 'https://s3.example', $3, $4)`, "mnt_"+name, name, name+"-key", actor)
	}
	exec(`INSERT INTO projects (id, slug, name) VALUES ('prj_l', 'l', 'L')`)
	exec(`INSERT INTO pipeline_runs (id, project_id, pipeline, source, definition, actor, state)
		VALUES ('plr_l', 'prj_l', 'ingest', 'template', '{}', $1, 'done')`, actor)
	seg, cut := "b3:"+strings.Repeat("1", 64), "b3:"+strings.Repeat("2", 64)
	// ingest names mount calls and produces seg; cut reads seg and produces cut.
	exec(`INSERT INTO pipeline_steps (id, pipeline_run_id, project_id, step, position, kind, kind_version, params, inputs)
		VALUES ('pls_ingest', 'plr_l', 'prj_l', 'ingest', 0, 'sdp_ingest', '1', '{"path":"mount://calls/2026/"}', '{}'),
		       ('pls_cut', 'plr_l', 'prj_l', 'cut', 1, 'segments_cut', '1', '{}', $1)`,
		`{"segments":{"hash":"`+seg+`","type":"segments"}}`)
	exec(`INSERT INTO artifacts (hash, type, size, project_id, pipeline_run_id, step_id, step, output)
		VALUES ($1, 'segments', 1, 'prj_l', 'plr_l', 'pls_ingest', 'ingest', 'segments'),
		       ($2, 'segments', 1, 'prj_l', 'plr_l', 'pls_cut', 'cut', 'segments')`, seg, cut)

	for _, c := range []struct {
		name string
		spec steps.Spec
		want []string
	}{
		{"names nothing, reads nothing", steps.Spec{Kind: "text_normalise", Resources: steps.Resources{JobKind: steps.JobData}}, nil},
		{"names a mount", steps.Spec{Kind: "noise_mine", Params: []byte(`{"root":"mount://other/x"}`),
			Resources: steps.Resources{JobKind: steps.JobData}}, []string{"other"}},
		{"reads what the ingest named, two steps back", steps.Spec{Kind: "pseudolabel_ensemble",
			Inputs: map[string]steps.ArtifactRef{"segments": {Hash: cut, Type: "segments"}}, Resources: steps.Resources{JobKind: steps.JobData}},
			[]string{"calls"}},
		{"training names nothing", steps.Spec{Kind: "train", Resources: steps.Resources{JobKind: steps.JobTraining}}, nil},
	} {
		list, env, err := ForLease(ctx, pool, c.spec)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range list {
			if m.CredentialsEnv != "" {
				got = append(got, m.Name)
				if env[m.CredentialsEnv] != m.Name+"-key" {
					t.Errorf("%s: env %v", c.name, env)
				}
			}
		}
		if len(list) != 2 || !slices.Equal(got, c.want) || len(env) != len(c.want) {
			t.Errorf("%s: credentials for %v (env %v), want %v", c.name, got, env, c.want)
		}
	}
}
