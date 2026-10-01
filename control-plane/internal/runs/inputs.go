package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	datasets "github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/mixes"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Artifact types a run's pipeline inputs are filled with, by the type the pipeline declares.
const (
	TypeMix           = "mix"
	TypeDataset       = "dataset"
	TypeBaseModel     = "base_model"
	TypeCheckpoint    = "checkpoint"
	TypeCalibration   = "calibration"
	TypeTrainingState = "training-state"
)

// MixRef names the mix revision a run trains on and its rendered artifact (R13).
type MixRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Hash     string `json:"hash"`
}

// RenderedMix is a mix revision rendered as a `mix` artifact in the content store.
type RenderedMix struct {
	Ref      MixRef
	Artifact steps.ArtifactRef
	// Datasets are the dataset artifacts of the mix by dataset version id, in group order.
	Datasets []DatasetArtifact
	Data     Data
}

// DatasetArtifact is one dataset version of a mix with the artifact training reads.
type DatasetArtifact struct {
	VersionID string
	Name      string
	Version   string
	Artifact  steps.ArtifactRef
}

// ResolveMix reads mix ref (mix_… or its name) of the project at revision rev (0: its current revision).
func ResolveMix(ctx context.Context, q storage.Querier, projectID, ref string, rev int) (string, mixes.Content, int, error) {
	if ref == "" {
		return "", mixes.Content{}, 0, problems.Validation([]problems.FieldError{{Path: "/mix", Message: "name the mix to train on (mix_… or its name)"}})
	}
	var m mixes.Mix
	var err error
	if strings.HasPrefix(ref, "mix_") {
		m, err = mixes.Get(ctx, q, ref)
	} else {
		var id string
		qerr := q.QueryRow(ctx, "SELECT id FROM mixes WHERE project_id = $1 AND content->>'name' = $2", projectID, ref).Scan(&id)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return "", mixes.Content{}, 0, problems.NotFound.New("the project has no mix named %q", ref)
		}
		if qerr != nil {
			return "", mixes.Content{}, 0, fmt.Errorf("find mix %q: %w", ref, qerr)
		}
		m, err = mixes.Get(ctx, q, id)
	}
	if err != nil {
		return "", mixes.Content{}, 0, err
	}
	if m.ProjectID != projectID {
		return "", mixes.Content{}, 0, problems.NotFound.New("the project has no mix %q", ref)
	}
	if rev == 0 || rev == m.Rev {
		return m.ID, m.Content, m.Rev, nil
	}
	raw, err := mixes.RevisionContent(ctx, q, m.ID, rev)
	if err != nil {
		return "", mixes.Content{}, 0, err
	}
	var c mixes.Content
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", mixes.Content{}, 0, fmt.Errorf("decode mix %s revision %d: %w", m.ID, rev, err)
	}
	return m.ID, c, rev, nil
}

// mixDoc is the content of a `mix` artifact: the resolved input_cfg of a mix revision with the dataset artifacts
// training reads. Its hash is the content hash R13 asks a run to record.
type mixDoc struct {
	Format      string     `json:"format"`
	Mix         mixDocRef  `json:"mix"`
	Temperature float64    `json:"temperature"`
	ReplayShare float64    `json:"replayShare"`
	InputCfg    []mixGroup `json:"input_cfg"`
}

type mixDocRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
}

type mixGroup struct {
	Type        string       `json:"type"` // group
	Name        string       `json:"name"`
	Replay      bool         `json:"replay"`
	Weight      float64      `json:"weight"`
	Probability float64      `json:"probability"` // share of the samples the group gets (temperature applied)
	InputCfg    []mixDataset `json:"input_cfg"`
}

type mixDataset struct {
	Type     string  `json:"type"` // dataset
	Dataset  string  `json:"dataset"`
	Name     string  `json:"name"`
	Version  string  `json:"version"`
	Artifact string  `json:"artifact"`
	Hours    float64 `json:"hours"`
}

// datasetArtifact is the content-store hash of a dataset version's `dataset` artifact. The dataset output hook
// registers it as an artifact reference ({hash, type, size}, the contract's DatasetVersion.artifact); a bare hash
// string is accepted too.
type datasetArtifact string

func (a *datasetArtifact) UnmarshalJSON(b []byte) error {
	var hash string
	if err := json.Unmarshal(b, &hash); err == nil {
		*a = datasetArtifact(hash)
		return nil
	}
	var ref steps.ArtifactRef
	if err := json.Unmarshal(b, &ref); err != nil {
		return fmt.Errorf("artifact: neither a hash nor an artifact reference: %w", err)
	}
	*a = datasetArtifact(ref.Hash)
	return nil
}

// MixFormat is the format tag of a mix artifact.
const MixFormat = "cadence.mix/1"

// RenderMix resolves every dataset version of c (trainable now, with a content-store artifact), renders the mix
// artifact and puts it in the store. A phase-1 fixture version without an artifact cannot be trained on.
func RenderMix(ctx context.Context, q storage.Querier, store *cas.Store, projectID, id string, c mixes.Content, rev int, bytesPerHour int64) (RenderedMix, error) {
	if store == nil {
		return RenderedMix{}, errors.New("runs: no content store")
	}
	meta, err := mixes.LoadDatasets(ctx, q, projectID, c)
	if err != nil {
		return RenderedMix{}, err
	}
	pv := mixes.ComputePreview(c, meta)
	share := map[string]float64{}
	for _, g := range pv.Groups {
		share[g.Name] = g.Share
	}
	out := RenderedMix{Data: Data{Datasets: []string{}}}
	doc := mixDoc{Format: MixFormat, Mix: mixDocRef{ID: id, Name: c.Name, Revision: rev}, Temperature: c.Temperature, ReplayShare: c.ReplayShare}
	var fields []problems.FieldError
	for gi, g := range c.Groups {
		mg := mixGroup{Type: "group", Name: g.Name, Replay: g.Replay, Weight: g.Weight, Probability: share[g.Name], InputCfg: []mixDataset{}}
		for di, vid := range g.Datasets {
			v, err := registry.GetVersion(ctx, q, registry.KindDataset, vid)
			if err != nil {
				return RenderedMix{}, err
			}
			if err := datasets.Trainable(ctx, q, v); err != nil {
				return RenderedMix{}, err
			}
			var p struct {
				Artifact datasetArtifact `json:"artifact"`
				Hours    float64         `json:"hours"`
				Bytes    int64           `json:"bytes"`
			}
			if err := json.Unmarshal(v.Payload, &p); err != nil {
				return RenderedMix{}, fmt.Errorf("decode dataset %s: %w", v.ID, err)
			}
			if !steps.ValidHash(string(p.Artifact)) {
				fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/mix/groups/%d/datasets/%d", gi, di),
					Message: fmt.Sprintf("%s %s has no content-store artifact (a phase-1 fixture); import the data with pipelines/import and mix the imported version", v.Name, v.Version)})
				continue
			}
			mg.InputCfg = append(mg.InputCfg, mixDataset{Type: "dataset", Dataset: v.ID, Name: v.Name, Version: v.Version, Artifact: string(p.Artifact), Hours: meta[v.ID].Hours})
			ref, err := sizedRef(ctx, q, store, steps.ArtifactRef{Hash: string(p.Artifact), Type: TypeDataset})
			if err != nil {
				return RenderedMix{}, err
			}
			out.Datasets = append(out.Datasets, DatasetArtifact{VersionID: v.ID, Name: v.Name, Version: v.Version, Artifact: ref})
			if p.Bytes == 0 {
				p.Bytes = int64(p.Hours * float64(bytesPerHour))
			}
			out.Data.Datasets = append(out.Data.Datasets, v.ID)
			out.Data.Hours += p.Hours
			out.Data.Bytes += p.Bytes
		}
		doc.InputCfg = append(doc.InputCfg, mg)
	}
	if len(fields) > 0 {
		return RenderedMix{}, problems.Validation(fields)
	}
	out.Data.Hours = round(out.Data.Hours, 3)
	b, err := json.Marshal(doc)
	if err != nil {
		return RenderedMix{}, fmt.Errorf("render mix: %w", err)
	}
	hash, err := store.PutBytes(b)
	if err != nil {
		return RenderedMix{}, err
	}
	m, _ := json.Marshal(map[string]any{"format": MixFormat, "mixId": id, "name": c.Name, "revision": rev,
		"datasets": out.Data.Datasets, "hours": out.Data.Hours})
	out.Ref = MixRef{ID: id, Name: c.Name, Revision: rev, Hash: hash}
	out.Artifact = steps.ArtifactRef{Hash: hash, Type: TypeMix, Size: int64(len(b)), Meta: m}
	return out, nil
}

// sizedRef completes ref with the size the artifact index (or, for an unindexed blob, the store) knows; recording
// it as a pipeline input checks the size.
func sizedRef(ctx context.Context, q storage.Querier, store *cas.Store, ref steps.ArtifactRef) (steps.ArtifactRef, error) {
	a, err := artifacts.Get(ctx, q, ref.Hash)
	if err == nil {
		ref.Size = a.Size
		if len(ref.Meta) == 0 {
			ref.Meta = a.Meta
		}
		return ref, nil
	}
	if pe, ok := problems.As(err); !ok || pe.Type != problems.NotFound {
		return ref, err
	}
	if store != nil {
		if has, size, err := store.Has(ref.Hash); err == nil && has {
			ref.Size = size
		}
	}
	return ref, nil
}

// BaseModelFormat is the format tag of a base_model artifact.
const BaseModelFormat = "cadence.base_model/1"

// RenderBaseModel puts the base_model artifact of version v (Hugging Face repository and revision, the file a run
// initialises from, the family reference) in the store.
func RenderBaseModel(store *cas.Store, v registry.Version, f Family) (steps.ArtifactRef, error) {
	var p map[string]any
	if err := json.Unmarshal(v.Payload, &p); err != nil {
		return steps.ArtifactRef{}, fmt.Errorf("decode base model %s: %w", v.ID, err)
	}
	doc := map[string]any{
		"format": BaseModelFormat, "versionId": v.ID, "collection": v.Name, "version": v.Version,
		"family": map[string]string{"name": f.Name, "versionId": f.VersionID}, "model": p,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return steps.ArtifactRef{}, fmt.Errorf("render base model: %w", err)
	}
	hash, err := store.PutBytes(b)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	meta, _ := json.Marshal(map[string]any{"format": BaseModelFormat, "collection": v.Name, "versionId": v.ID,
		"hfRepo": p["hfRepo"], "revision": p["revision"], "family": f.Name})
	return steps.ArtifactRef{Hash: hash, Type: TypeBaseModel, Size: int64(len(b)), Meta: meta}, nil
}

// start is what a run's pipeline inputs are filled from.
type start struct {
	Base       steps.ArtifactRef  // the base model artifact
	Checkpoint *steps.ArtifactRef // init checkpoint: the checkpoint artifact
	Mix        RenderedMix
}

// inputsFor fills each declared pipeline input from s by its artifact type: mix → the rendered mix; dataset → the
// mix's only dataset; base_model → the base model, or the start checkpoint for init checkpoint (a checkpoint may
// feed a base_model input, pipelines.Accepts); checkpoint → the start checkpoint. Anything else is a recipe that
// does not fit a run.
func inputsFor(declared map[string]string, s start) (map[string]steps.ArtifactRef, error) {
	out := map[string]steps.ArtifactRef{}
	var bad []string
	for _, name := range sortedKeys(declared) {
		typ := declared[name]
		switch typ {
		case TypeMix:
			out[name] = s.Mix.Artifact
		case TypeDataset:
			if len(s.Mix.Datasets) != 1 {
				bad = append(bad, fmt.Sprintf("input %q takes one dataset, but the mix has %d dataset versions (declare a mix input instead)", name, len(s.Mix.Datasets)))
				continue
			}
			out[name] = s.Mix.Datasets[0].Artifact
		case TypeBaseModel:
			if s.Checkpoint != nil {
				out[name] = *s.Checkpoint
			} else {
				out[name] = s.Base
			}
		case TypeCheckpoint:
			if s.Checkpoint == nil {
				bad = append(bad, fmt.Sprintf("input %q takes a checkpoint, but the run starts from the base model (init base)", name))
				continue
			}
			out[name] = *s.Checkpoint
		default:
			bad = append(bad, fmt.Sprintf("input %q of type %s cannot be filled by a run (it fills mix, dataset, base_model and checkpoint inputs)", name, typ))
		}
	}
	if len(bad) > 0 {
		return nil, problems.RecipeMismatch.New("%s", strings.Join(bad, "; "))
	}
	return out, nil
}
