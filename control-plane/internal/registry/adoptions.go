package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// AliasKind is the EntityKind of project aliases.
const AliasKind = "alias"

// Alias reservations (R8), as the contract's Alias.reserved.
const (
	AliasFree      = "free"      // aliases.set moves it
	AliasGated     = "gated"     // aliases.set needs an approval: the policy engine gates it (baseline)
	AliasPromotion = "promotion" // only deployments.promote moves it (production)
)

// Reserved alias names (R8).
const (
	AliasBaseline   = "baseline"
	AliasProduction = "production"
)

// Reservation tells how an alias may move.
func Reservation(name string) string {
	switch name {
	case AliasBaseline:
		return AliasGated
	case AliasProduction:
		return AliasPromotion
	}
	return AliasFree
}

// Gated reports whether aliases.set on name needs an approval (R8). The contract marks the same rule on the
// operation as x-cadence.gatedWhen {param: name, values: [baseline]} for the policy engine.
func Gated(name string) bool { return Reservation(name) == AliasGated }

// Adoption is a registry version a project adopted.
type Adoption struct {
	ProjectID string
	Version   Version
	Actor     auth.Actor
	AdoptedAt time.Time
	Aliases   []string
}

// Adopt makes a frozen version available to the project at revision rev; the project moves to rev+1. The version
// must pass the licence check, and for a target adoption (purpose "" or target) the locale check (checks.go).
func Adopt(ctx context.Context, tx pgx.Tx, slug string, rev int, versionID, purpose string, actor auth.Actor) (Adoption, projects.Project, []events.Draft, error) {
	p, err := projects.Touch(ctx, tx, slug, rev)
	if err != nil {
		return Adoption{}, projects.Project{}, nil, err
	}
	if p.ArchivedAt != nil {
		return Adoption{}, projects.Project{}, nil, problems.Conflict.New("project %q is archived; nothing can be adopted into it", slug)
	}
	v, err := GetVersion(ctx, tx, "", versionID)
	if err != nil {
		return Adoption{}, projects.Project{}, nil, err
	}
	switch v.State {
	case StateDraft:
		return Adoption{}, projects.Project{}, nil, problems.Conflict.New("%s %s is a draft; only frozen versions can be adopted", v.Name, v.Version)
	case StateDeprecated:
		return Adoption{}, projects.Project{}, nil, problems.Conflict.New("%s %s is deprecated; adopt a newer version of %s", v.Name, v.Version, v.Name)
	case StateArchived:
		return Adoption{}, projects.Project{}, nil, problems.Conflict.New("%s %s is archived; adopt another version of %s", v.Name, v.Version, v.Name)
	}
	if Published(v.Kind) {
		return Adoption{}, projects.Project{}, nil, problems.Conflict.New("%s %s is published by workers; pipelines pin %ss as name@version and nothing adopts them",
			v.Name, v.Version, Noun(v.Kind))
	}
	if purpose != "" && purpose != PurposeTarget && purpose != PurposeReplay {
		return Adoption{}, projects.Project{}, nil, problems.BadRequest.New("purpose %q is not target or replay", purpose)
	}
	if err := CheckLicence(v); err != nil {
		return Adoption{}, projects.Project{}, nil, err
	}
	if err := CheckLocale(v, p.Locales, purpose); err != nil {
		return Adoption{}, projects.Project{}, nil, err
	}
	a := Adoption{ProjectID: p.ID, Version: v, Actor: actor, Aliases: []string{}}
	err = tx.QueryRow(ctx, `INSERT INTO adoptions (project_id, version_id, adopted_by) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING RETURNING adopted_at`, p.ID, v.ID, actor).Scan(&a.AdoptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Adoption{}, projects.Project{}, nil, problems.Conflict.New("project %q already adopted %s %s", slug, v.Name, v.Version)
	}
	if err != nil {
		return Adoption{}, projects.Project{}, nil, fmt.Errorf("insert adoption: %w", err)
	}
	return a, p, projects.Event(p, "project.adopted", map[string]any{
		"adoption": map[string]any{"version": v.Summary(), "adoptedAt": a.AdoptedAt, "purpose": or(purpose, PurposeTarget)},
	}), nil
}

// ListAdoptions returns the project's adoptions, newest first, optionally of one kind.
func ListAdoptions(ctx context.Context, q storage.Querier, projectID, kind string) ([]Adoption, error) {
	rows, err := q.Query(ctx, `SELECT a.version_id, a.adopted_by, a.adopted_at,
			coalesce(array_agg(al.name ORDER BY al.name) FILTER (WHERE al.name IS NOT NULL), '{}')
		FROM adoptions a LEFT JOIN aliases al ON al.project_id = a.project_id AND al.version_id = a.version_id
		WHERE a.project_id = $1 GROUP BY a.version_id, a.adopted_by, a.adopted_at ORDER BY a.adopted_at DESC, a.version_id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list adoptions: %w", err)
	}
	var (
		out []Adoption
		ids []string
		a   Adoption
		vid string
	)
	if _, err := pgx.ForEachRow(rows, []any{&vid, &a.Actor, &a.AdoptedAt, &a.Aliases}, func() error {
		a.ProjectID, a.Version.ID = projectID, vid
		out, ids = append(out, a), append(ids, vid)
		a = Adoption{}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read adoptions: %w", err)
	}
	versions, err := ListVersions(ctx, q, Filter{IDs: ids, Kind: kind})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Version, len(versions))
	for _, v := range versions {
		byID[v.ID] = v
	}
	kept := out[:0]
	for _, a := range out {
		if v, ok := byID[a.Version.ID]; ok {
			a.Version = v
			kept = append(kept, a)
		}
	}
	return kept, nil
}

// Alias is a project's named pointer at an adopted version.
type Alias struct {
	ID        string
	Name      string
	ProjectID string
	Version   Version
	Rev       int
	Actor     auth.Actor
	UpdatedAt time.Time
}

type aliasRow struct {
	Alias
	versionID string
}

func aliases(ctx context.Context, q storage.Querier, projectID, name, lock string) ([]Alias, error) {
	rows, err := q.Query(ctx, `SELECT id, name, project_id, version_id, rev, set_by, updated_at FROM aliases
		WHERE project_id = $1 AND ($2 = '' OR name = $2) ORDER BY name `+lock, projectID, name)
	if err != nil {
		return nil, fmt.Errorf("query aliases: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (aliasRow, error) {
		var a aliasRow
		return a, row.Scan(&a.ID, &a.Name, &a.ProjectID, &a.versionID, &a.Rev, &a.Actor, &a.UpdatedAt)
	})
	if err != nil {
		return nil, fmt.Errorf("read aliases: %w", err)
	}
	ids := make([]string, 0, len(list))
	for _, a := range list {
		ids = append(ids, a.versionID)
	}
	versions, err := ListVersions(ctx, q, Filter{IDs: ids})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Version, len(versions))
	for _, v := range versions {
		byID[v.ID] = v
	}
	out := make([]Alias, 0, len(list))
	for _, a := range list {
		a.Version = byID[a.versionID]
		out = append(out, a.Alias)
	}
	return out, nil
}

// ListAliases returns the project's aliases by name.
func ListAliases(ctx context.Context, q storage.Querier, projectID string) ([]Alias, error) {
	return aliases(ctx, q, projectID, "", "")
}

// GetAlias returns one alias of the project, or not-found.
func GetAlias(ctx context.Context, q storage.Querier, projectID, name string) (Alias, error) {
	list, err := aliases(ctx, q, projectID, name, "")
	if err != nil {
		return Alias{}, err
	}
	if len(list) == 0 {
		return Alias{}, problems.NotFound.New("the project has no alias @%s", name)
	}
	return list[0], nil
}

// SetAlias points the project's alias name at an adopted, frozen version. Without ifMatch it only creates (an
// existing alias answers precondition-required); with ifMatch the alias must exist at that revision.
// production is refused with reserved-alias (R8); gating baseline is the policy engine's job (see Gated).
func SetAlias(ctx context.Context, tx pgx.Tx, projectID, name string, ifMatch *int, versionID string, actor auth.Actor) (Alias, []events.Draft, error) {
	if Reservation(name) == AliasPromotion {
		return Alias{}, nil, problems.ReservedAlias.New(
			"@%s moves only with a promotion (deployments.promote); aliases.set cannot move it", name)
	}
	v, err := GetVersion(ctx, tx, "", versionID)
	if err != nil {
		return Alias{}, nil, err
	}
	if v.State != StateFrozen {
		return Alias{}, nil, problems.Conflict.New("%s %s is %s; an alias points only at a frozen version", v.Name, v.Version, v.State)
	}
	var adopted bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM adoptions WHERE project_id = $1 AND version_id = $2)",
		projectID, v.ID).Scan(&adopted); err != nil {
		return Alias{}, nil, fmt.Errorf("check adoption: %w", err)
	}
	if !adopted {
		return Alias{}, nil, problems.Conflict.New("the project has not adopted %s %s; adopt it first (projects.adopt)", v.Name, v.Version)
	}

	cur, err := aliases(ctx, tx, projectID, name, "FOR UPDATE")
	if err != nil {
		return Alias{}, nil, err
	}
	var (
		id       string
		previous string
	)
	switch {
	case len(cur) == 0 && ifMatch != nil:
		return Alias{}, nil, problems.Stale(0, "alias @%s does not exist yet; omit If-Match to create it", name)
	case len(cur) == 0:
		id = "als_" + uuid.Must(uuid.NewV7()).String()
		tag, err := tx.Exec(ctx, `INSERT INTO aliases (id, project_id, name, version_id, set_by) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (project_id, name) DO NOTHING`, id, projectID, name, v.ID, actor)
		if err != nil {
			return Alias{}, nil, fmt.Errorf("insert alias: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return Alias{}, nil, problems.Stale(1, "alias @%s was just created by another request; re-read it and retry", name)
		}
	case ifMatch == nil:
		return Alias{}, nil, commands.Precondition("alias")
	default:
		if err := commands.CheckRev(AliasKind, *ifMatch, cur[0].Rev); err != nil {
			return Alias{}, nil, err
		}
		id, previous = cur[0].ID, cur[0].Version.ID
		if _, err := tx.Exec(ctx, `UPDATE aliases SET version_id = $2, set_by = $3, rev = rev + 1, updated_at = now()
			WHERE id = $1`, id, v.ID, actor); err != nil {
			return Alias{}, nil, fmt.Errorf("update alias: %w", err)
		}
	}
	a, err := GetAlias(ctx, tx, projectID, name)
	if err != nil {
		return Alias{}, nil, err
	}
	payload := map[string]any{"id": a.ID, "name": a.Name, "rev": a.Rev, "version": a.Version.Summary()}
	if previous != "" {
		payload["previousVersionId"] = previous
	}
	return a, []events.Draft{{
		Topic:     events.EntityTopic(AliasKind, a.ID),
		Type:      "alias.set",
		ProjectID: projectID,
		Entity:    &events.EntityRef{Kind: AliasKind, ID: a.ID, Rev: a.Rev},
		Payload:   map[string]any{"alias": payload},
	}}, nil
}

// Resolve finds the version a reference names for a project: ver_… (by id), @alias (the project's alias) or a
// collection name — the version of it the project adopted (what data.lock lists; the newest adopted when several),
// else the collection's newest frozen version. The version must be of kind.
func Resolve(ctx context.Context, q storage.Querier, projectID, kind, ref string) (Version, error) {
	switch {
	case strings.HasPrefix(ref, "@"):
		a, err := GetAlias(ctx, q, projectID, strings.TrimPrefix(ref, "@"))
		if err != nil {
			return Version{}, err
		}
		if a.Version.Kind != kind {
			return Version{}, problems.Conflict.New("%s points at a %s, not a %s", ref, Noun(a.Version.Kind), Noun(kind))
		}
		return a.Version, nil
	case strings.HasPrefix(ref, "ver_"):
		return GetVersion(ctx, q, kind, ref)
	default:
		if projectID != "" {
			if v, ok, err := Adopted(ctx, q, projectID, kind, ref); err != nil || ok {
				return v, err
			}
		}
		return Latest(ctx, q, kind, ref)
	}
}

// Adopted returns the newest version of the collection (name or id) that the project adopted — the one its
// data.lock pins — and false when it adopted none. Archived versions do not count.
func Adopted(ctx context.Context, q storage.Querier, projectID, kind, collection string) (Version, bool, error) {
	list, err := ListVersions(ctx, q, Filter{Kind: kind, Collection: collection, ProjectID: projectID, HideArchived: true, Limit: 1})
	if err != nil || len(list) == 0 {
		return Version{}, false, err
	}
	return list[0], true, nil
}

// AdoptQuietly records that the project uses these frozen versions, without a revision or an event of its own:
// projects.new, the bootstrap job and a template sync call it for the versions they write into data.lock, inside a
// command or job that emits its own events. Versions already adopted are skipped; it returns how many were added.
func AdoptQuietly(ctx context.Context, tx pgx.Tx, projectID string, versionIDs []string, actor auth.Actor) (int, error) {
	added := 0
	for _, id := range versionIDs {
		tag, err := tx.Exec(ctx, `INSERT INTO adoptions (project_id, version_id, adopted_by)
			SELECT $1, v.id, $3 FROM registry_versions v WHERE v.id = $2 AND v.state = 'frozen'
			ON CONFLICT DO NOTHING`, projectID, id, actor)
		if err != nil {
			return added, fmt.Errorf("adopt %s: %w", id, err)
		}
		added += int(tag.RowsAffected())
	}
	return added, nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
