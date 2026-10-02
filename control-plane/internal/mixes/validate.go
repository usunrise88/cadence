package mixes

import (
	"context"
	"fmt"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// GroupInput is a group as a client sends it: weight and replay may be omitted, datasets are references.
type GroupInput struct {
	Name     string
	Weight   *float64
	Replay   *bool
	Datasets []string // ver_…, @alias or a collection name
}

// Input is a whole mix as a client sends it (mixes.new, mixes.preview, and an edit applied to the current content).
type Input struct {
	Name        string
	Description string
	Groups      []GroupInput
	Temperature *float64
	ReplayShare *float64
}

// EditInput is the body of mixes.edit: nil fields stay as they are; Groups replaces the whole list.
type EditInput struct {
	Name, Description        *string
	Groups                   *[]GroupInput
	Temperature, ReplayShare *float64
}

// Input returns c as an input (every value set), the starting point of an edit.
func (c Content) Input() Input {
	in := Input{Name: c.Name, Description: c.Description, Temperature: ptr(c.Temperature), ReplayShare: ptr(c.ReplayShare)}
	for _, g := range c.Groups {
		in.Groups = append(in.Groups, GroupInput{Name: g.Name, Weight: ptr(g.Weight), Replay: ptr(g.Replay), Datasets: append([]string(nil), g.Datasets...)})
	}
	return in
}

// Apply returns in with the edit's fields replaced.
func (in Input) Apply(e EditInput) Input {
	if e.Name != nil {
		in.Name = *e.Name
	}
	if e.Description != nil {
		in.Description = *e.Description
	}
	if e.Groups != nil {
		in.Groups = *e.Groups
	}
	if e.Temperature != nil {
		in.Temperature = e.Temperature
	}
	if e.ReplayShare != nil {
		in.ReplayShare = e.ReplayShare
	}
	return in
}

func ptr[T any](v T) *T { return &v }

// Normalize fills in defaults (defaults.yaml mix.*), resolves dataset references for the project and validates the
// result: every dataset version exists, is a dataset and is frozen; group names and datasets are unique; values
// lie inside the ranges of defaults.yaml; a replay share needs a replay group and a mix needs a non-replay group.
// Every problem is reported as a field error of validation-failed.
func Normalize(ctx context.Context, q storage.Querier, projectID string, in Input, d *defaults.Defaults) (Content, error) {
	var errs []problems.FieldError
	fail := func(path, format string, a ...any) {
		errs = append(errs, problems.FieldError{Path: path, Message: fmt.Sprintf(format, a...)})
	}
	c := Content{Name: strings.TrimSpace(in.Name), Description: in.Description, Temperature: d.Mix.Temperature.Value}
	if c.Name == "" {
		fail("/name", "must not be empty")
	}
	if in.Temperature != nil {
		c.Temperature = *in.Temperature
	}
	if err := d.Mix.Temperature.Range.Check(c.Temperature); err != nil {
		fail("/temperature", "%v (defaults.yaml mix.temperature)", err)
	}
	if len(in.Groups) == 0 {
		fail("/groups", "a mix needs at least one group")
	}
	names := map[string]bool{}
	used := map[string]string{} // version id → group path
	var evalOnly, leaked, overlaps []problems.FieldError
	replay, plain := 0, 0
	for i, g := range in.Groups {
		p := fmt.Sprintf("/groups/%d", i)
		out := Group{Name: strings.TrimSpace(g.Name), Weight: d.Mix.GroupWeight.Value, Replay: g.Replay != nil && *g.Replay}
		key := strings.ToLower(out.Name)
		switch {
		case out.Name == "":
			fail(p+"/name", "must not be empty")
		case names[key]:
			fail(p+"/name", "another group is already named %q", out.Name)
		}
		names[key] = true
		if g.Weight != nil {
			out.Weight = *g.Weight
		}
		if err := d.Mix.GroupWeight.Range.Check(out.Weight); err != nil || out.Weight <= 0 {
			fail(p+"/weight", "must be above 0 and inside the range of defaults.yaml mix.group_weight")
		}
		if out.Replay {
			replay++
		} else {
			plain++
		}
		if len(g.Datasets) == 0 {
			fail(p+"/datasets", "a group needs at least one dataset version")
		}
		for j, ref := range g.Datasets {
			dp := fmt.Sprintf("%s/datasets/%d", p, j)
			v, err := resolve(ctx, q, projectID, strings.TrimSpace(ref))
			if err != nil {
				pe, ok := problems.As(err)
				if !ok {
					return Content{}, err
				}
				fail(dp, "%s", pe.Detail)
				continue
			}
			if v.State != registry.StateFrozen {
				fail(dp, "%s %s is %s; only frozen dataset versions can be mixed", v.Name, v.Version, v.State)
				continue
			}
			if err := data.Trainable(ctx, q, v); err != nil {
				pe, ok := problems.As(err)
				if !ok {
					return Content{}, err
				}
				fail(dp, "%s", pe.Detail)
				switch pe.Type {
				case problems.EvalOnlyDataset:
					evalOnly = append(evalOnly, errs[len(errs)-1])
				case problems.GoldenSetLeakage:
					leaked = append(leaked, errs[len(errs)-1])
					for _, o := range pe.Errors {
						overlaps = append(overlaps, problems.FieldError{Path: fmt.Sprintf("/overlaps/%d", len(overlaps)), Message: o.Message})
					}
				}
				continue
			}
			if prev, dup := used[v.ID]; dup {
				fail(dp, "%s %s is already in %s; a dataset version belongs to one group", v.Name, v.Version, prev)
				continue
			}
			used[v.ID] = p
			out.Datasets = append(out.Datasets, v.ID)
		}
		c.Groups = append(c.Groups, out)
	}
	if len(in.Groups) > 0 && plain == 0 {
		fail("/groups", "a mix needs at least one group that is not replay")
	}
	switch {
	case in.ReplayShare != nil:
		c.ReplayShare = *in.ReplayShare
	case replay > 0:
		c.ReplayShare = d.Mix.ReplayShare.Value
	}
	if err := d.Mix.ReplayShare.Range.Check(c.ReplayShare); err != nil {
		fail("/replayShare", "%v (defaults.yaml mix.replay_share)", err)
	} else if c.ReplayShare > 0 && replay == 0 {
		fail("/replayShare", "is %g but no group is a replay group (replay: true); set it to 0 or mark a group", c.ReplayShare)
	}
	if len(evalOnly) > 0 {
		// A mix is training data: an eval-only version is refused with its own type, whatever else is wrong.
		pe := problems.EvalOnlyDataset.New("the mix is not valid: %s %s", evalOnly[0].Path, evalOnly[0].Message)
		pe.Errors = errs
		return Content{}, pe
	}
	if len(leaked) > 0 {
		// Golden-set audio never reaches training: refused with its own type, listing every overlap after the fields.
		pe := problems.GoldenSetLeakage.New("the mix is not valid: %s %s", leaked[0].Path, leaked[0].Message)
		pe.Errors = append(errs, overlaps...)
		return Content{}, pe
	}
	if len(errs) > 0 {
		pe := problems.Validation(errs)
		pe.Detail = "the mix is not valid: " + errs[0].Path + " " + errs[0].Message
		return Content{}, pe
	}
	return c, nil
}

// resolve finds the dataset version a reference names: ver_…, @alias or a collection name (newest frozen version).
func resolve(ctx context.Context, q storage.Querier, projectID, ref string) (registry.Version, error) {
	if ref == "" {
		return registry.Version{}, problems.NotFound.New("empty dataset reference")
	}
	if !strings.HasPrefix(ref, "ver_") && !strings.HasPrefix(ref, "@") && !strings.Contains(ref, "/") {
		ref = "dataset/" + ref // a bare collection name
	}
	return registry.Resolve(ctx, q, projectID, registry.KindDataset, ref)
}
