// Package search is the search index and its query language (docs/spec/11-ui-panels.md "Search").
//
// The index (search_documents) is a projection of the outbox: Indexer reloads each entity an event names through
// the registration table (Sources) and upserts its document; help articles are indexed at start. Search runs a
// parsed Query against it with Postgres full-text search (the 'simple' configuration over normalised text,
// prefix matching) and pg_trgm word similarity for typos and identifiers, within what the caller may see
// (Visibility), and groups the hits by kind, the current project's newest first.
package search

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
)

// SimilarityThreshold is the pg_trgm word similarity above which a word counts as a typo of a query word.
// Cadence recommendation: 0.4 finds one transposition or one wrong letter in a six-letter word (flerus shares 3 of
// its 7 trigrams with fleurs: 0.43) while a three-letter word still needs two trigrams in common; the pg_trgm
// default (0.6) misses most single typos.
const SimilarityThreshold = 0.4

// minTrigramWord is the shortest query word matched by similarity; shorter words match by prefix only.
const minTrigramWord = 3

// Visibility is what one request may see: resolved from the credential's scope and the query's scope qualifiers
// by the caller, never from the query alone.
type Visibility struct {
	ProjectIDs  []string // project work of these projects
	AllProjects bool     // project work of every project (full scope with scope:all)
	Registry    bool
	Instance    bool
	Help        bool
}

// Request is one search.
type Request struct {
	Query   Query
	Visible Visibility
	// CurrentProjectID ranks first.
	CurrentProjectID string
	// AliasProjectIDs are the projects whose aliases alias: reads; nil with AllProjects means every project.
	AliasProjectIDs []string
	Limit           int
}

// Hit is one result.
type Hit struct {
	Document
	Snippet     string
	ProjectSlug string
}

// Group is the hits of one kind, in rank order.
type Group struct {
	Kind string
	Hits []Hit
}

// Result is a search's answer.
type Result struct {
	Groups    []Group
	Total     int
	Truncated bool
}

func (v Visibility) empty() bool {
	return !v.AllProjects && len(v.ProjectIDs) == 0 && !v.Registry && !v.Instance && !v.Help
}

// Search runs req in a read-only transaction (which carries the similarity threshold).
func Search(ctx context.Context, pool *pgxpool.Pool, req Request) (Result, error) {
	if req.Limit <= 0 {
		req.Limit = 50
	}
	if req.Visible.empty() {
		return Result{}, nil
	}
	sql, args := buildSQL(req)
	var hits []Hit
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('pg_trgm.word_similarity_threshold', $1, true)`,
			fmt.Sprint(SimilarityThreshold)); err != nil {
			return fmt.Errorf("set similarity threshold: %w", err)
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}
		hits, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Hit, error) {
			var (
				h         Hit
				projectID *string
				slug      *string
				actor     *auth.Actor
			)
			err := row.Scan(&h.Kind, &h.ID, &h.Scope, &projectID, &slug, &h.Ref, &h.Title, &h.Text, &h.Tags, &h.Status,
				&h.Lang, &actor, &h.Numbers, &h.UpdatedAt)
			if projectID != nil {
				h.ProjectID = *projectID
			}
			if slug != nil {
				h.ProjectSlug = *slug
			}
			h.Actor = actor
			return h, err
		})
		if err != nil {
			return fmt.Errorf("read search hits: %w", err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	out := Result{}
	if len(hits) > req.Limit {
		hits, out.Truncated = hits[:req.Limit], true
	}
	index := map[string]int{}
	for _, h := range hits {
		h.Snippet = snippet(h.Text, req.Query.Terms)
		h.Text = ""
		i, ok := index[h.Kind]
		if !ok {
			i = len(out.Groups)
			index[h.Kind] = i
			out.Groups = append(out.Groups, Group{Kind: h.Kind})
		}
		out.Groups[i].Hits = append(out.Groups[i].Hits, h)
	}
	out.Total = len(hits)
	return out, nil
}

// buildSQL renders req as one query: visibility, filters and text conditions; ordered by where the hit lives
// (current project, registry and instance, other projects, help), then whether every word matched the full-text
// index (not only by similarity), then recency.
func buildSQL(req Request) (string, []any) {
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	q := req.Query

	var vis []string
	switch {
	case req.Visible.AllProjects:
		vis = append(vis, "d.scope = 'project'")
	case len(req.Visible.ProjectIDs) > 0:
		vis = append(vis, "(d.scope = 'project' AND d.project_id = ANY("+arg(req.Visible.ProjectIDs)+"))")
	}
	if req.Visible.Registry {
		vis = append(vis, "d.scope = 'registry'")
	}
	if req.Visible.Instance {
		vis = append(vis, "d.scope = 'instance'")
	}
	if req.Visible.Help {
		vis = append(vis, "d.scope = 'help'")
	}
	conds := []string{"(" + strings.Join(vis, " OR ") + ")"}

	if len(q.Kinds) > 0 {
		conds = append(conds, "d.kind = ANY("+arg(q.Kinds)+")")
	}
	if len(q.Statuses) > 0 {
		conds = append(conds, "lower(d.status) = ANY("+arg(q.Statuses)+")")
	}
	if len(q.Langs) > 0 {
		var ors []string
		for _, l := range q.Langs {
			p := arg(l)
			ors = append(ors, fmt.Sprintf("(lower(d.lang) = %[1]s OR lower(d.lang) LIKE %[1]s || '-%%' OR %[1]s LIKE lower(d.lang) || '-%%')", p))
		}
		conds = append(conds, "("+strings.Join(ors, " OR ")+")")
	}
	if len(q.Actors) > 0 {
		var kinds, ids []string
		for _, a := range q.Actors {
			switch strings.ToLower(a) {
			case auth.KindAgent, auth.KindUser, auth.KindAutomation:
				kinds = append(kinds, strings.ToLower(a))
			default:
				ids = append(ids, a)
			}
		}
		var ors []string
		if len(kinds) > 0 {
			ors = append(ors, "d.actor_kind = ANY("+arg(kinds)+")")
		}
		if len(ids) > 0 {
			p := arg(ids)
			ors = append(ors, "d.actor->>'id' = ANY("+p+") OR d.actor->>'name' = ANY("+p+") OR d.actor->>'sessionId' = ANY("+p+")")
		}
		conds = append(conds, "("+strings.Join(ors, " OR ")+")")
	}
	for _, b := range q.Updated {
		if !b.From.IsZero() {
			conds = append(conds, "d.updated_at >= "+arg(b.From))
		}
		if !b.Before.IsZero() {
			conds = append(conds, "d.updated_at < "+arg(b.Before))
		}
	}
	for _, t := range q.Tags {
		p := arg(t)
		conds = append(conds, fmt.Sprintf("EXISTS (SELECT 1 FROM unnest(d.tags) t WHERE lower(t) = %[1]s OR lower(t) LIKE '%%:' || %[1]s)", p))
	}
	if len(q.Aliases) > 0 {
		c := "EXISTS (SELECT 1 FROM aliases a WHERE a.version_id = d.id AND lower(a.name) = ANY(" + arg(lower(q.Aliases)) + ")"
		if !(req.Visible.AllProjects && req.AliasProjectIDs == nil) {
			c += " AND a.project_id = ANY(" + arg(nonNil(req.AliasProjectIDs)) + ")"
		}
		conds = append(conds, c+")")
	}
	for _, n := range q.Numbers {
		f := arg(n.Field)
		val := "(d.numbers->>" + f + ")::float8"
		c := "(d.numbers->>" + f + ") IS NOT NULL AND "
		if n.Op == OpRange {
			c += val + " BETWEEN " + arg(n.Value) + " AND " + arg(n.Value2)
		} else {
			c += val + " " + n.Op + " " + arg(n.Value)
		}
		conds = append(conds, "("+c+")")
	}

	var exact []string
	for _, t := range q.Terms {
		if t.Phrase {
			c := "(d.title_norm || ' ' || d.body_norm) LIKE '%' || " + arg(escapeLike(t.Text)) + " || '%'"
			conds = append(conds, c)
			continue
		}
		fts := "d.tsv @@ to_tsquery('simple', " + arg("'"+t.Text+"':*") + ")"
		exact = append(exact, fts)
		if utf8.RuneCountInString(t.Text) >= minTrigramWord {
			p := arg(t.Text)
			conds = append(conds, "("+fts+" OR d.title_norm %> "+p+" OR d.body_norm %> "+p+")")
		} else {
			conds = append(conds, fts)
		}
	}

	bucket := "CASE WHEN d.scope = 'help' THEN 3 WHEN d.project_id IS NULL THEN 1 ELSE 2 END"
	if req.CurrentProjectID != "" {
		bucket = "CASE WHEN d.project_id = " + arg(req.CurrentProjectID) + " THEN 0 WHEN d.scope = 'help' THEN 3 WHEN d.project_id IS NULL THEN 1 ELSE 2 END"
	}
	order := bucket
	if len(exact) > 0 {
		order += ", (" + strings.Join(exact, " AND ") + ") DESC"
	}
	sql := `SELECT d.kind, d.id, d.scope, d.project_id, p.slug, d.ref, d.title, left(d.body, 4000), d.tags, d.status,
			d.lang, d.actor, d.numbers, d.updated_at
		FROM search_documents d LEFT JOIN projects p ON p.id = d.project_id
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY ` + order + `, d.updated_at DESC, d.kind, d.id
		LIMIT ` + arg(req.Limit+1)
	return sql, args
}

func lower(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToLower(s)
	}
	return out
}

func nonNil(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

const snippetRunes = 160

// snippet cuts a short excerpt of text around the first query term it contains, or its start.
func snippet(text string, terms []Term) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	low := strings.ToLower(text)
	at := -1
	for _, t := range terms {
		if i := strings.Index(low, t.Text); i >= 0 && (at < 0 || i < at) && len(low) == len(text) {
			at = i
		}
	}
	runes := []rune(text)
	start := 0
	if at > 0 {
		start = max(utf8.RuneCountInString(text[:at])-40, 0)
	}
	end := min(start+snippetRunes, len(runes))
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

// DocumentCount returns how many documents the index holds (metrics and tests).
func DocumentCount(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM search_documents").Scan(&n); err != nil {
		return 0, fmt.Errorf("count search documents: %w", err)
	}
	return n, nil
}

// Cursor returns the indexer's position in the outbox (tests and health).
func Cursor(ctx context.Context, pool *pgxpool.Pool) (int64, time.Time, error) {
	var (
		seq int64
		at  time.Time
	)
	err := pool.QueryRow(ctx, "SELECT seq, updated_at FROM event_cursors WHERE name = $1", CursorName).Scan(&seq, &at)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("read search cursor: %w", err)
	}
	return seq, at, nil
}
