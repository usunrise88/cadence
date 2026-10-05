package modelexports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/delivery"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// The smoke set of a delivery bundle (02 "Delivery bundle", D3's delivery.Sources.Smoke): up to delivery.MaxSmoke
// utterances of the export's parity sample with what the staging server wrote for them, and the family's smoke
// client. The parity reference wrote the inputs the client sends (smoke_inputs: for a family whose caller sends
// features, the feature buffers), parity_score@1 copied them into its report with the served side's output as the
// expected text (token ids when the served hypotheses carry them), and the export wrote the client into the
// deployable (deployable.json smoke.client). Go copies files and strings; it never reads them.

// parityReportSmoke is the smoke part of report.json (cadence.parity/1).
type parityReportSmoke struct {
	Smoke struct {
		Format string `json:"format"`
		Items  []struct {
			Name     string `json:"name"`
			Audio    string `json:"audio"`
			File     string `json:"file"`
			Expected string `json:"expected"`
		} `json:"items"`
	} `json:"smoke"`
}

// deployableSmoke is the smoke part of deployable.json.
type deployableSmoke struct {
	Smoke struct {
		Client string `json:"client"`
		Input  string `json:"input"`
	} `json:"smoke"`
}

// Smoke returns the smoke set of the export of a deployable artifact: its newest finished parity check's report
// (the newest that passed, else the newest) and the deployable's smoke client.
func (s *Service) Smoke(ctx context.Context, q storage.Querier, versionID, deployableHash string) (delivery.Smoke, error) {
	list, err := queryExports(ctx, q, "deployable_hash = $1 AND ($2 = '' OR model_version_id = $2) ORDER BY updated_at DESC LIMIT 1",
		deployableHash, versionID)
	if err != nil {
		return delivery.Smoke{}, err
	}
	if len(list) == 0 {
		return delivery.Smoke{}, fmt.Errorf("no model export has the deployable %s", deployableHash)
	}
	x := list[0]
	checks, err := queryChecks(ctx, q, `export_id = $1 AND kind = 'parity' AND state = 'done' AND report_hash IS NOT NULL
		ORDER BY (result ->> 'verdict' = 'passed') DESC, finished_at DESC LIMIT 1`, x.ID)
	if err != nil {
		return delivery.Smoke{}, err
	}
	if len(checks) == 0 {
		return delivery.Smoke{}, fmt.Errorf("export %s has no finished parity check: its smoke set is the parity sample (models.parity)", x.ID)
	}
	report := checks[0].ReportHash
	m, err := s.CAS.ReadManifest(report)
	if err != nil {
		return delivery.Smoke{}, fmt.Errorf("parity report %s is not a directory: %w", report, err)
	}
	byPath := map[string]cas.File{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	rb, err := s.dirFile(report, "report.json", 64<<20)
	if err != nil {
		return delivery.Smoke{}, err
	}
	var rs parityReportSmoke
	if err := json.Unmarshal(rb, &rs); err != nil {
		return delivery.Smoke{}, fmt.Errorf("parity report %s: %w", report, err)
	}
	var out delivery.Smoke
	for _, it := range rs.Smoke.Items {
		if len(out.Items) == delivery.MaxSmoke {
			break
		}
		f, ok := byPath[strings.TrimPrefix(it.File, "./")]
		if !ok {
			return delivery.Smoke{}, fmt.Errorf("parity report %s lists smoke file %s it does not hold", report, it.File)
		}
		name := it.Name
		if name == "" {
			name = path.Base(it.File)
		}
		out.Items = append(out.Items, delivery.SmokeItem{Name: name, Hash: f.Hash, Text: it.Expected})
	}
	if len(out.Items) == 0 {
		return delivery.Smoke{}, fmt.Errorf("parity report %s holds no smoke set (its reference step wrote no smoke inputs)", report)
	}
	db, err := s.dirFile(x.DeployableHash, "deployable.json", 16<<20)
	if err != nil {
		return delivery.Smoke{}, err
	}
	var ds deployableSmoke
	if err := json.Unmarshal(db, &ds); err != nil {
		return delivery.Smoke{}, fmt.Errorf("deployable.json of %s: %w", x.DeployableHash, err)
	}
	if ds.Smoke.Client == "" {
		return delivery.Smoke{}, errors.New("the deployable names no smoke client (deployable.json smoke.client)")
	}
	if out.Client, err = s.dirFile(x.DeployableHash, strings.TrimPrefix(ds.Smoke.Client, "./"), 16<<20); err != nil {
		return delivery.Smoke{}, err
	}
	return out, nil
}
