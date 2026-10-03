package cache

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// datasetHashSQL is a dataset version payload's artifact hash: an artifact reference ({hash, type, size}) or, in
// older payloads, a bare hash string.
const datasetHashSQL = `coalesce(v.payload->'artifact'->>'hash', CASE WHEN jsonb_typeof(v.payload->'artifact') = 'string' THEN v.payload->>'artifact' END)`

var versionIDRe = regexp.MustCompile(`ver_[0-9a-f-]{36}`)

// Touch records that a lease reads spec's inputs now (LRU): the input artifacts themselves, and the dataset
// artifacts of every dataset version the spec names (a mix input names its versions in its metadata).
func Touch(ctx context.Context, tx pgx.Tx, spec steps.Spec, now time.Time) error {
	hashes := make([]string, 0, len(spec.Inputs))
	var ids []string
	for _, in := range spec.Inputs {
		hashes = append(hashes, in.Hash)
		ids = append(ids, versionIDRe.FindAllString(string(in.Meta), -1)...)
	}
	ids = append(ids, versionIDRe.FindAllString(string(spec.Params), -1)...)
	if len(hashes) == 0 && len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE artifacts SET last_used_at = $3
		WHERE evicted_at IS NULL AND (hash = ANY($1) OR hash IN (SELECT `+datasetHashSQL+` FROM registry_versions v WHERE v.id = ANY($2)))`,
		hashes, ids, now); err != nil {
		return fmt.Errorf("record cache use: %w", err)
	}
	return nil
}
