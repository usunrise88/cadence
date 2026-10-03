package mounts

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// CheckKind is the core step kind that checks a mount's health on a worker (worker/cadence_worker/steps/mount_check.py).
const (
	CheckKind        = "mount_check"
	CheckKindVersion = "1"
)

// LeaseMount is a mount as a lease carries it (the contract's LeaseMount): what cadence_worker.mounts needs to
// resolve mount://<name>/<path> on the worker.
type LeaseMount struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Root           string `json:"root"`
	ReadOnly       bool   `json:"readOnly"`
	Endpoint       string `json:"endpoint,omitempty"`
	Region         string `json:"region,omitempty"`
	Revision       string `json:"revision,omitempty"`
	CredentialsEnv string `json:"credentialsEnv,omitempty"`
}

// CredentialsEnv is the lease environment variable that holds a mount's credentials:
// CADENCE_MOUNT_<NAME>_CREDENTIALS, the name upper-cased with dashes as underscores.
func CredentialsEnv(name string) string {
	return "CADENCE_MOUNT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_CREDENTIALS"
}

var namedRe = regexp.MustCompile(`mount://([a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?)/`)

// Named lists the mounts a step spec names: every mount://<name>/ in its params and its inputs' metadata, sorted.
func Named(spec steps.Spec) []string {
	var docs []string
	docs = append(docs, string(spec.Params))
	for _, in := range spec.Inputs {
		docs = append(docs, string(in.Meta))
	}
	var out []string
	for _, d := range docs {
		for _, m := range namedRe.FindAllStringSubmatch(d, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	sort.Strings(out)
	return out
}

// readsMounts reports whether a step of this job kind may read any mount's credentials: data, eval and export
// steps read audio where it lives (evaluation may read remotely; spec 02 "Materialisation"); training reads the
// cache only.
func readsMounts(jobKind string) bool {
	return jobKind == steps.JobData || jobKind == steps.JobEval || jobKind == steps.JobExport
}

// ForLease lists every mount for the lease of spec, and which secrets go into the lease environment (env variable →
// secret name): a mount's credentials reach a step that names the mount, a data, eval or export step, and the
// mount's own health check — never a training step that does not name it. Values are read by the worker protocol,
// which already holds the secret store; this package never reads one.
func ForLease(ctx context.Context, q storage.Querier, spec steps.Spec) ([]LeaseMount, map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT name, kind, root, read_only, endpoint, region, revision, credentials FROM mounts ORDER BY name`)
	if err != nil {
		return nil, nil, fmt.Errorf("read mounts: %w", err)
	}
	defer rows.Close()
	named := Named(spec)
	out := []LeaseMount{}
	env := map[string]string{}
	for rows.Next() {
		var (
			m     LeaseMount
			cred  string
			check bool
		)
		if err := rows.Scan(&m.Name, &m.Kind, &m.Root, &m.ReadOnly, &m.Endpoint, &m.Region, &m.Revision, &cred); err != nil {
			return nil, nil, fmt.Errorf("read mounts: %w", err)
		}
		check = spec.Kind == CheckKind && slices.Contains(named, m.Name)
		if cred != "" && (check || slices.Contains(named, m.Name) || readsMounts(spec.Resources.JobKind)) {
			m.CredentialsEnv = CredentialsEnv(m.Name)
			env[m.CredentialsEnv] = cred
		}
		out = append(out, m)
	}
	return out, env, rows.Err()
}

// CheckJob fails with mount-unhealthy when spec names a mount whose last health check failed: a job never starts on
// an unhealthy mount (spec 02 "Storage and mounts"). A mount never checked passes; the health check itself always
// runs. A name that is no registered mount is left to the step (it fails resolving the URI).
func CheckJob(ctx context.Context, q storage.Querier, spec steps.Spec) error {
	if spec.Kind == CheckKind {
		return nil
	}
	named := Named(spec)
	if len(named) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT name, health FROM mounts WHERE name = ANY($1) AND health->>'state' = $2 ORDER BY name`,
		named, HealthUnhealthy)
	if err != nil {
		return fmt.Errorf("check mount health: %w", err)
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var (
			name string
			raw  []byte
			h    Health
		)
		if err := rows.Scan(&name, &raw); err != nil {
			return fmt.Errorf("check mount health: %w", err)
		}
		_ = json.Unmarshal(raw, &h)
		why := name
		if h.Detail != "" {
			why += " (" + h.Detail + ")"
		}
		bad = append(bad, why)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check mount health: %w", err)
	}
	if len(bad) > 0 {
		return problems.MountUnhealthy.New("the step reads mount %s, whose last health check failed; fix it and run mounts.verify",
			strings.Join(bad, ", "))
	}
	return nil
}
