package registry

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
)

//go:embed fixtures/*.yaml
var fixtures embed.FS

// Bundled is the actor that bundled and fixture versions are attributed to.
func Bundled() auth.Actor {
	return auth.Actor{Kind: auth.KindAutomation, ID: "cadence", Name: "Cadence (bundled)"}
}

// Seed registers, frozen, every bundled version the registry does not hold yet: the base-model catalogue, the
// fixture dataset versions and the scoring normalizers (internal/registry/fixtures) and each unit of the templates
// tree. It is idempotent —
// unchanged content is never registered twice — and returns how many versions it added.
func Seed(ctx context.Context, pool *pgxpool.Pool, templates fs.FS, now time.Time) (int, error) {
	inputs, err := BundledInputs(templates)
	if err != nil {
		return 0, err
	}
	added := 0
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var drafts []events.Draft
		for _, in := range inputs {
			_, created, d, err := Register(ctx, tx, in, now)
			if err != nil {
				return fmt.Errorf("seed %s: %w", in.Name, err)
			}
			if created {
				added++
				drafts = append(drafts, d...)
			}
		}
		return events.Append(ctx, tx, Bundled(), nil, drafts)
	})
	if err != nil {
		return 0, fmt.Errorf("seed registry: %w", err)
	}
	return added, nil
}

// BundledInputs lists the versions Seed registers, in a stable order.
func BundledInputs(templates fs.FS) ([]RegisterInput, error) {
	var out []RegisterInput
	for _, f := range []struct{ file, kind string }{
		{"fixtures/base-models.yaml", KindBaseModel},
		{"fixtures/datasets.yaml", KindDataset},
		{"fixtures/normalizers.yaml", KindNormalizer},
		{"fixtures/auxiliaries.yaml", KindAuxiliary},
	} {
		ins, err := fixtureInputs(f.file, f.kind)
		if err != nil {
			return nil, err
		}
		out = append(out, ins...)
	}
	ins, err := TemplateInputs(templates)
	if err != nil {
		return nil, err
	}
	return append(out, ins...), nil
}

type fixture struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Licence     string         `yaml:"licence"`
	Tags        []string       `yaml:"tags"`
	Payload     map[string]any `yaml:"payload"`
}

func fixtureInputs(file, kind string) ([]RegisterInput, error) {
	raw, err := fixtures.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	var list []fixture
	if err := yaml.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	out := make([]RegisterInput, 0, len(list))
	for _, f := range list {
		payload, err := json.Marshal(f.Payload)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", file, f.Name, err)
		}
		out = append(out, RegisterInput{
			Kind: kind, Name: f.Name, Description: f.Description, Tags: f.Tags, Licence: f.Licence,
			Payload: payload, Actor: Bundled(), Freeze: true,
		})
	}
	return out, nil
}

// templateDirs maps each top-level directory of the templates tree to its template kind and collection prefix.
var templateDirs = map[string]string{
	"instructions": "instructions",
	"presets":      "preset",
	"skills":       "skill",
	"pipelines":    "pipeline",
	"agent-config": "agent-config",
	"playbooks":    "playbook",
	"lang":         "langpack",   // lang/<locale>/: starter language packs (template/langpack-he-il)
	"annotation":   "annotation", // annotation/guidelines/: the annotation guidelines (template/annotation-guidelines, R27)
}

// TemplateFile is one file of a template version.
type TemplateFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// TemplatePayload is the content of a template version (the contract's TemplatePayload): which files, with their
// hashes. The files themselves ship in the binary.
type TemplatePayload struct {
	TemplateKind string         `json:"templateKind"`
	Path         string         `json:"path"`
	Files        []TemplateFile `json:"files"`
}

// TemplateInputs lists one template version per entry under each known top-level directory of the tree: a
// directory (instructions/default, skills/cadence-data) or a file (pipelines/train-stage.yaml, presets/<name>.yaml).
// File contents are opaque here; a changed file makes a new version.
func TemplateInputs(tree fs.FS) ([]RegisterInput, error) {
	var out []RegisterInput
	top, err := fs.ReadDir(tree, ".")
	if err != nil {
		return nil, fmt.Errorf("read templates: %w", err)
	}
	for _, d := range top {
		kind, ok := templateDirs[d.Name()]
		if !ok || !d.IsDir() {
			continue
		}
		entries, err := fs.ReadDir(tree, d.Name())
		if err != nil {
			return nil, fmt.Errorf("read templates/%s: %w", d.Name(), err)
		}
		for _, e := range entries {
			unit := path.Join(d.Name(), e.Name())
			files, err := templateFiles(tree, unit)
			if err != nil {
				return nil, err
			}
			base, _, _ := strings.Cut(e.Name(), ".")
			payload, err := json.Marshal(TemplatePayload{TemplateKind: kind, Path: unit, Files: files})
			if err != nil {
				return nil, fmt.Errorf("template %s: %w", unit, err)
			}
			out = append(out, RegisterInput{
				Kind: KindTemplate, Name: "template/" + kind + "-" + strings.ToLower(base),
				Description: fmt.Sprintf("Bundled %s template %s (templates/%s)", kind, base, unit),
				Tags:        []string{kind}, Licence: "internal", Payload: payload, Actor: Bundled(), Freeze: true,
			})
		}
	}
	return out, nil
}

func templateFiles(tree fs.FS, root string) ([]TemplateFile, error) {
	var files []TemplateFile
	err := fs.WalkDir(tree, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(tree, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		files = append(files, TemplateFile{Path: p, SHA256: hex.EncodeToString(sum[:]), Bytes: len(b)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read templates/%s: %w", root, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}
