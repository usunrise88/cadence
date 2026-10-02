//go:build integration

package search

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/experiments"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// TestIndexesEvaluationKinds: golden sets, normalizers and models (registry) and evals and experiments (project
// work) join the index, kind:model finds registered models (not base models) and kind:base-model still finds base
// models.
func TestIndexesEvaluationKinds(t *testing.T) {
	ctx := context.Background()
	pool := openDB(t)
	if _, err := registry.Seed(ctx, pool, templates.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := newProject(t, pool, "hebrew", "Hebrew calls")
	for _, in := range []registry.RegisterInput{
		{Kind: registry.KindGoldenSet, Name: "golden-set/callcenter-he", Payload: []byte(`{"note":"call center golden"}`)},
		{Kind: registry.KindNormalizer, Name: "normalizer/strict-he", Payload: []byte(`{"rules":"strip punctuation"}`)},
		{Kind: registry.KindModel, Name: "model/hebrew-calls", Payload: []byte(`{"card":"fine-tuned on calls"}`)},
	} {
		in.Actor, in.Freeze = auth.DevActor(), true
		command(t, pool, func(tx pgx.Tx) ([]events.Draft, error) {
			_, _, d, err := registry.Register(ctx, tx, in, time.Now())
			return d, err
		})
	}

	var baseID string
	if err := pool.QueryRow(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.kind = 'base_model' LIMIT 1`).Scan(&baseID); err != nil {
		t.Fatal(err)
	}
	command(t, pool, func(tx pgx.Tx) ([]events.Draft, error) {
		actor := `{"kind":"user","id":"usr_admin"}`
		if _, err := tx.Exec(ctx, `INSERT INTO mixes (id, project_id, content, created_by, updated_by)
			VALUES ('mix_1', $1, '{"name":"calls"}', $2, $2)`, p.ID, actor); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO experiments (id, project_id, name, question, tag, mix_id, mix_rev, base_version_id, actor)
			VALUES ('exp_1', $1, 'Replay share', 'Does more replay keep FLEURS flat?', 'replay-share', 'mix_1', 1, $2, $3)`,
			p.ID, baseID, actor); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO evals (id, project_id, status, subject, subject_id, baseline, golden_sets, profiles,
				decoding, significance, estimate, gate, actor)
			VALUES ('evl_1', $1, 'done', '{"kind":"checkpoint","id":"ckp_1","label":"calls step 4000"}', 'ckp_1',
				'{"kind":"base_model","id":"ver_b","label":"nemotron base"}',
				'[{"versionId":"ver_g","name":"golden-set/callcenter-he","version":"2026-10-01.abcdefabcdef","locale":"he-IL"}]',
				'[{"name":"balanced","latencyMs":560}]', '[]', '{}', '{}', '{"verdict":"passed"}', $2)`, p.ID, actor); err != nil {
			return nil, err
		}
		return []events.Draft{
			{Topic: experiments.Topic("exp_1"), Type: experiments.EventCreated, ProjectID: p.ID,
				Entity: &events.EntityRef{Kind: experiments.Kind, ID: "exp_1", Rev: 1}},
			{Topic: events.EntityTopic(evals.Kind, "evl_1"), Type: evals.EventGated, ProjectID: p.ID,
				Entity: &events.EntityRef{Kind: evals.Kind, ID: "evl_1", Rev: 1}},
		}, nil
	})
	if _, err := NewIndexer(pool, nil, quiet, Sources()).CatchUp(ctx); err != nil {
		t.Fatal(err)
	}

	all := Visibility{ProjectIDs: []string{p.ID}, Registry: true}
	for _, c := range []struct{ q, kind, title string }{
		{"kind:golden-set callcenter", registry.KindGoldenSet, ""},
		{"kind:normalizer strict", registry.KindNormalizer, ""},
		{"kind:model calls", registry.KindModel, ""},
		{"kind:eval calls", evals.Kind, "Eval of calls step 4000 vs nemotron base"},
		{"kind:eval tag:passed", evals.Kind, "Eval of calls step 4000 vs nemotron base"},
		{"kind:experiment replay", experiments.Kind, "Replay share"},
		{"fleurs", experiments.Kind, "Replay share"},
	} {
		r := find(t, pool, c.q, all, p.ID)
		found := false
		for _, g := range r.Groups {
			for _, h := range g.Hits {
				if h.Kind == c.kind && (c.title == "" || h.Title == c.title) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%q did not find a %s %q: %v", c.q, c.kind, c.title, titles(r))
		}
	}
	if r := find(t, pool, "kind:model", all, p.ID); len(r.Groups) != 1 || r.Groups[0].Kind != registry.KindModel {
		t.Errorf("kind:model should find registered models only: %v", titles(r))
	}
	if r := find(t, pool, "kind:base-model", all, p.ID); r.Total == 0 || r.Groups[0].Kind != registry.KindBaseModel {
		t.Errorf("kind:base-model should find the bundled base models: %v", titles(r))
	}
	if r := find(t, pool, "kind:eval", Visibility{Registry: true}, ""); r.Total != 0 {
		t.Errorf("evals are project work, not registry: %v", titles(r))
	}
}
