//go:build integration

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
)

// A step that names a mount URI is reused only while the files under it are unchanged (owner decision of 2026-10-04):
// the same run again is reused, a touched or an added file runs the step again, and the fingerprint is on the step.
func TestMountContentIsInTheInputHash(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.RegisterKinds(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	audio := filepath.Join(root, "corpus", "rev1")
	if err := os.MkdirAll(audio, 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(audio, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.wav", "RIFF-a")
	write("a.txt", "shalom")
	if _, err := e.pool.Exec(ctx, `INSERT INTO mounts (id, name, kind, root, created_by)
		VALUES ('mnt_fx', 'fx', 'local', $1, '{"kind":"user","id":"usr_admin"}')`, root); err != nil {
		t.Fatal(err)
	}
	e.newProject("ingest")
	// echo's prefix stands in for an ingest's path: any mount:// URI among a step's parameters is read.
	e.commitPipeline("ingest", "read-mount", "name: read-mount\ninputs: {text: text}\nsteps:\n"+
		"  - {id: read, kind: echo@1, in: {text: $inputs.text}, params: {prefix: \"mount://fx/corpus/rev1\"}}\n")
	text := e.putText("x")
	run := func() (state, fingerprint string) {
		t.Helper()
		var r pipelineRunView
		e.ok(e.do("POST", "/api/projects/ingest/pipelines/read-mount:run", `{"inputs":{"text":`+text+`}}`,
			"Idempotency-Key", e.key(), "If-Match", "*"), 201, &r)
		r = e.waitPipelineRun(r.ID, "done")
		if err := e.pool.QueryRow(ctx, "SELECT coalesce(mount_fingerprint, '') FROM pipeline_steps WHERE pipeline_run_id = $1",
			r.ID).Scan(&fingerprint); err != nil {
			t.Fatal(err)
		}
		return r.Steps[0].State, fingerprint
	}

	first, fp1 := run()
	if first != "done" || len(fp1) != len("sha256:")+64 {
		t.Fatalf("first run: %s, fingerprint %q", first, fp1)
	}
	if again, fp := run(); again != "reused" || fp != fp1 {
		t.Fatalf("unchanged files: %s (fingerprint %q, was %q)", again, fp, fp1)
	}
	// A touched transcript sidecar (same size, new mtime) runs the step again.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(audio, "a.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	touched, fp2 := run()
	if touched != "done" || fp2 == fp1 {
		t.Fatalf("touched file: %s (fingerprint %q)", touched, fp2)
	}
	write("b.wav", "RIFF-b")
	if added, fp3 := run(); added != "done" || fp3 == fp2 {
		t.Fatalf("added file: %s (fingerprint %q)", added, fp3)
	}
	if again, _ := run(); again != "reused" {
		t.Fatalf("unchanged again: %s", again)
	}
}
