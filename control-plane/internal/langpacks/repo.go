package langpacks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Read is a pack as read from a project repository.
type Read struct {
	Pack
	Commit string // the commit the ref resolved to
	SHA    string // the last commit at Commit that changed the pack: its version
}

// Dirs lists the pack directories (names under lang/) of a repository at ref, with the commit ref resolved to.
func Dirs(ctx context.Context, st *repos.Store, slug, ref string) ([]string, string, error) {
	commit, files, err := st.ListFiles(ctx, slug, ref, Dir)
	if err != nil {
		return nil, commit, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, f := range files {
		rest, ok := strings.CutPrefix(f.Path, Dir+"/")
		if !ok {
			continue
		}
		name, _, nested := strings.Cut(rest, "/")
		if nested && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, commit, nil
}

// ReadPack reads the pack of locale (its directory lang/<locale>) at ref. A locale without a pack is not-found.
func ReadPack(ctx context.Context, st *repos.Store, slug, ref, locale string) (Read, error) {
	if !ValidLocale(locale) {
		return Read{}, problems.Validation([]problems.FieldError{{Path: "locale", Message: fmt.Sprintf("%q is not a locale (he-IL, sr)", locale)}})
	}
	if dirs, _, err := Dirs(ctx, st, slug, ref); err == nil {
		for _, d := range dirs {
			if strings.EqualFold(d, locale) {
				locale = d // the directory's spelling (he-IL asked as he-il)
			}
		}
	}
	dir := PackPath(locale)
	commit, files, err := st.ListFiles(ctx, slug, ref, dir)
	if err != nil {
		if errors.Is(err, repos.ErrNotFound) {
			return Read{}, problems.NotFound.New("%v", err)
		}
		return Read{}, err
	}
	if len(files) == 0 {
		return Read{}, problems.NotFound.New("project %s has no language pack for %s (lang/%s/); langpacks.list names the packs it has and the ones Cadence ships, and projects.sync adds a shipped pack for every project locale", slug, locale, locale)
	}
	r := Read{Pack: Pack{Locale: locale, Files: map[string][]byte{}}, Commit: commit}
	for _, f := range files {
		b, _, err := st.ReadFile(ctx, slug, commit, f.Path)
		if err != nil {
			return Read{}, err
		}
		r.Files[strings.TrimPrefix(f.Path, dir+"/")] = b
	}
	hist, err := st.History(ctx, slug, commit, dir, 1)
	if err != nil {
		return Read{}, err
	}
	if len(hist) > 0 {
		r.SHA = hist[0].SHA
	}
	return r, nil
}

// ResolveScoring returns the registry normalizer version a scoring reference names: a version id, or a collection's
// newest frozen version.
func ResolveScoring(ctx context.Context, q storage.Querier, ref string) (registry.Version, error) {
	if strings.HasPrefix(ref, "ver_") {
		return registry.GetVersion(ctx, q, registry.KindNormalizer, ref)
	}
	return registry.Latest(ctx, q, registry.KindNormalizer, strings.ToLower(ref)) // collection names are lower case
}
