package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/drafts"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/langpacks"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/bootstrap"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Language packs and boost lists (phase 3 · stream L; docs/spec/03-pipelines-defaults.md "Language packs and hot
// words"): lang/<locale>/ in the project repository, read and committed like recipes.

func (c commandResponse) VisitLangpacksEditResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitBoostEditResponse(w http.ResponseWriter) error     { return c.write(w) }

// packLimits are the bounds pack checks apply (defaults.yaml langpacks).
func (s *Server) packLimits() langpacks.Limits {
	lp := s.defaultsDoc().Langpacks
	lim := langpacks.Limits{BoostMaxTerms: lp.BoostMaxTerms.Value, BoostMinWeight: 0, BoostMaxWeight: 10}
	if r := lp.BoostWeight.Range; r != nil {
		if r.Min != nil {
			lim.BoostMinWeight = *r.Min
		}
		if r.Max != nil {
			lim.BoostMaxWeight = *r.Max
		}
	}
	return lim
}

// scoringOf resolves the scoring normalizer a pack names (absent version when the registry has none yet).
func scoringOf(ctx context.Context, q storage.Querier, pk langpacks.Pack) (api.PackScoring, error) {
	n, err := pk.Normalizer()
	if err != nil {
		return api.PackScoring{}, nil // a broken normalizer.yaml is reported in issues, not fatal to a read
	}
	out := api.PackScoring{Normalizer: n.Scoring.Normalizer}
	if n.Scoring.Normalizer == "" {
		return out, nil
	}
	v, err := langpacks.ResolveScoring(ctx, q, n.Scoring.Normalizer)
	if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
		return out, nil
	}
	if err != nil {
		return api.PackScoring{}, err
	}
	out.VersionId, out.Version = optional(v.ID), optional(v.Version)
	return out, nil
}

// apiPack renders a pack read at commit whose version is sha.
func (s *Server) apiPack(ctx context.Context, pk langpacks.Pack, sha, commit string) (api.LanguagePack, error) {
	lim := s.packLimits()
	out := api.LanguagePack{Locale: pk.Locale, Path: langpacks.PackPath(pk.Locale), Sha: sha, Commit: commit,
		Files: []api.LanguagePackFile{}, Boost: []api.BoostList{}, Issues: []api.ProblemFieldError{}}
	for _, fp := range pk.Paths() {
		b := pk.Files[fp]
		out.Files = append(out.Files, api.LanguagePackFile{Path: fp, Bytes: len(b), Content: string(b)})
	}
	for _, b := range pk.BoostLists(lim.BoostMaxTerms) {
		out.Boost = append(out.Boost, api.BoostList{Domain: b.Domain, Path: langpacks.BoostPath(b.Domain), Weight: b.Weight,
			Terms: b.Terms, Sha256: b.SHA256()})
	}
	for _, f := range pk.Check(lim) {
		out.Issues = append(out.Issues, api.ProblemFieldError{Path: f.Path, Message: f.Message})
	}
	sc, err := scoringOf(ctx, s.Pool, pk)
	if err != nil {
		return api.LanguagePack{}, err
	}
	out.Scoring = sc
	return out, nil
}

// LangpacksList implements langpacks.list.
func (s *Server) LangpacksList(ctx context.Context, req api.LangpacksListRequestObject) (api.LangpacksListResponseObject, error) {
	p, svc, err := s.projectRepo(ctx, req.P)
	if err != nil {
		return nil, err
	}
	shipped, err := langpacks.Bundled(svc.Renderer().Tree)
	if err != nil {
		return nil, err
	}
	dirs, _, err := langpacks.Dirs(ctx, svc.Repos(), p.Slug, repos.Main)
	if err != nil {
		return nil, notFoundOr(err)
	}
	out := api.LangpacksList200JSONResponse{Items: []api.LanguagePackSummary{}, Shipped: shipped}
	for _, d := range dirs {
		r, err := langpacks.ReadPack(ctx, svc.Repos(), p.Slug, repos.Main, d)
		if err != nil {
			if pe, ok := problems.As(err); ok && (pe.Type == problems.NotFound || pe.Type == problems.ValidationFailed) {
				continue // lang/<not a locale>/ is not a pack
			}
			return nil, err
		}
		sc, err := scoringOf(ctx, s.Pool, r.Pack)
		if err != nil {
			return nil, err
		}
		item := api.LanguagePackSummary{Locale: r.Locale, Path: langpacks.PackPath(r.Locale), Sha: r.SHA, Files: len(r.Files),
			Boost: []string{}, Scoring: sc}
		for _, b := range r.BoostLists(0) {
			item.Boost = append(item.Boost, b.Domain)
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// LangpacksGet implements langpacks.get; the ETag is the pack's version (the last commit that changed it).
func (s *Server) LangpacksGet(ctx context.Context, req api.LangpacksGetRequestObject) (api.LangpacksGetResponseObject, error) {
	p, svc, err := s.projectRepo(ctx, req.P)
	if err != nil {
		return nil, err
	}
	r, err := langpacks.ReadPack(ctx, svc.Repos(), p.Slug, repos.Main, req.Locale)
	if err != nil {
		return nil, err
	}
	body, err := s.apiPack(ctx, r.Pack, r.SHA, r.Commit)
	if err != nil {
		return nil, err
	}
	etag := `"` + r.SHA + `"`
	return api.LangpacksGet200JSONResponse{Body: body, Headers: api.LangpacksGet200ResponseHeaders{ETag: &etag}}, nil
}

// LangpacksEdit implements langpacks.edit.
func (s *Server) LangpacksEdit(ctx context.Context, req api.LangpacksEditRequestObject) (api.LangpacksEditResponseObject, error) {
	expect, err := packVersionOf(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	w := bootstrap.PackWrite{Locale: req.Locale, Expect: expect, Files: map[string][]byte{}, Message: deref(req.Body.Message)}
	var fields []problems.FieldError
	seen := map[string]bool{}
	for _, f := range req.Body.Files {
		rel := strings.TrimPrefix(f.Path, langpacks.PackPath(req.Locale)+"/")
		if seen[rel] {
			fields = append(fields, problems.FieldError{Path: "files", Message: rel + " appears twice"})
		}
		seen[rel] = true
		switch {
		case deref(f.Delete) && f.Content != nil:
			fields = append(fields, problems.FieldError{Path: "files", Message: rel + ": send content or delete, not both"})
		case deref(f.Delete):
			w.Delete = append(w.Delete, rel)
		case f.Content == nil:
			fields = append(fields, problems.FieldError{Path: "files", Message: rel + ": send content (or delete: true)"})
		default:
			w.Files[rel] = []byte(*f.Content)
		}
	}
	if len(fields) > 0 {
		return nil, problems.Validation(fields)
	}
	return s.writePack(ctx, req.P, "langpacks.edit", req.Params.IdempotencyKey, req.Params.DryRun, w, nil)
}

// BoostEdit implements boost.edit: one list's terms and weight, rendered into boost/<domain>.txt.
func (s *Server) BoostEdit(ctx context.Context, req api.BoostEditRequestObject) (api.BoostEditResponseObject, error) {
	expect, err := packVersionOf(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	terms, err := langpacks.NormalizeTerms(req.Body.Terms)
	if err != nil {
		return nil, problems.Validation([]problems.FieldError{{Path: "terms", Message: err.Error()}})
	}
	w := bootstrap.PackWrite{Locale: req.Locale, Expect: expect, Message: deref(req.Body.Message)}
	if w.Message == "" {
		w.Message = "boost list " + req.Domain + " (" + req.Locale + ")"
	}
	rel := langpacks.BoostPath(req.Domain)
	// The list's file is rendered inside the command, against the pack as it is on main.
	render := func(cur langpacks.Pack) (map[string][]byte, error) {
		prev := cur.Files[rel]
		b := langpacks.Boost{Domain: req.Domain, Terms: terms, Weight: s.defaultsDoc().Langpacks.BoostWeight.Value}
		if old, err := langpacks.ParseBoost(req.Domain, prev, 0); err == nil {
			b.Weight = old.Weight
		}
		if req.Body.Weight != nil {
			b.Weight = *req.Body.Weight
		}
		return map[string][]byte{rel: b.Render(prev)}, nil
	}
	return s.writePack(ctx, req.P, "boost.edit", req.Params.IdempotencyKey, req.Params.DryRun, w, render)
}

// packVersionOf reads If-Match: the pack's version, a commit sha.
func packVersionOf(ifMatch string) (string, error) {
	v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ifMatch), "W/")), `"`)
	if len(v) < 7 || strings.Trim(v, "0123456789abcdef") != "" {
		return "", problems.BadRequest.New("If-Match must be the language pack's sha from langpacks.get (a commit sha), not %q", ifMatch)
	}
	return v, nil
}

// writePack runs a pack change as a command: render (when set) computes the files from the pack on main; an
// agent's change lands on a draft branch when the project's draft policy for language packs says draft.
func (s *Server) writePack(ctx context.Context, slug, op, key string, dry *bool, w bootstrap.PackWrite,
	render func(langpacks.Pack) (map[string][]byte, error)) (commandResponse, error) {
	_, svc, err := s.projectRepo(ctx, slug)
	if err != nil {
		return commandResponse{}, err
	}
	ctx, err = s.withProject(ctx, slug)
	if err != nil {
		return commandResponse{}, err
	}
	cmd := command(ctx, op, key, dry)
	w.Limits = s.packLimits()
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := projects.Get(ctx, tx, slug)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if render != nil {
			cur, err := langpacks.ReadPack(ctx, svc.Repos(), p.Slug, repos.Main, w.Locale)
			if err != nil {
				return commands.Result{}, nil, err
			}
			if w.Files, err = render(cur.Pack); err != nil {
				return commands.Result{}, nil, err
			}
		}
		if cmd.Actor.Kind == auth.KindAgent {
			policy, err := drafts.Policy(ctx, tx, s.defaultsDoc(), p.ID, "language_pack")
			if err != nil {
				return commands.Result{}, nil, err
			}
			w.Draft = policy == drafts.PolicyDraft
		}
		out, evs, err := svc.WritePack(ctx, tx, p, w, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		var body api.LanguagePack
		if out.Commit != "" {
			r, err := langpacks.ReadPack(ctx, svc.Repos(), p.Slug, out.Commit, out.Pack.Locale)
			if err != nil {
				return commands.Result{}, nil, err
			}
			if body, err = s.apiPack(ctx, r.Pack, r.SHA, r.Commit); err != nil {
				return commands.Result{}, nil, err
			}
		} else {
			head, _ := svc.Repos().Head(ctx, p.Slug)
			if body, err = s.apiPack(ctx, out.Pack, out.Base, head); err != nil {
				return commands.Result{}, nil, err
			}
		}
		if out.Branch != "" && out.Branch != repos.Main {
			body.Branch = optional(out.Branch)
		}
		return commands.Result{Status: http.StatusOK, Body: body, ETag: `"` + body.Sha + `"`}, evs, nil
	})
}
