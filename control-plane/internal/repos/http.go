package repos

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504 is httpoxy, fixed in Go 1.6.3: the handler drops the Proxy header
	"regexp"
	"strings"
)

// HTTPPath is where the bare repositories are served over git's smart HTTP protocol: /git/<slug>.git.
const HTTPPath = "/git"

// Access is what an authenticated git client may do with one repository.
type Access struct {
	// User names the client in the server log (REMOTE_USER).
	User string
	// Write allows pushes.
	Write bool
	// PushRefs, when set, is the only ref pattern (a shell pattern) the client may update, e.g.
	// refs/heads/session/<id> for an agent session token.
	PushRefs string
	// ReadOnly refuses every push with a message (the project is archived).
	ReadOnly bool
	// Principal is the caller's own record of who authenticated (handed back to the PushNotifier).
	Principal any
}

// Authorizer decides a request for a project's repository. token is the credential the client sent (the basic
// auth password, or a Bearer token); empty when it sent none. write reports a push. It returns an error that
// HTTPHandler maps: ErrUnauthorized (401 with a Basic challenge), ErrForbidden (403), ErrNotFound (404).
type Authorizer func(ctx context.Context, slug, token string, write bool) (Access, error)

// Errors an Authorizer returns.
var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
)

// PushNotifier is told which branches a push moved: name → [before, after] (zero-less: "" for created or deleted).
type PushNotifier func(ctx context.Context, slug string, access Access, moved map[string][2]string)

// HTTPHandler serves /git/<slug>.git/… through git http-backend (CGI). Clients authenticate with an API key or an
// agent session token as the basic-auth password (any user name, e.g. x-token) or as a Bearer token. After a push
// it compares the branches with what they were before and reports the moves to notify.
func (s *Store) HTTPHandler(authorize Authorizer, notify PushNotifier) http.Handler {
	return &httpHandler{s: s, authorize: authorize, notify: notify}
}

type httpHandler struct {
	s         *Store
	authorize Authorizer
	notify    PushNotifier
}

var gitPathRe = regexp.MustCompile(`^` + HTTPPath + `/([a-z][a-z0-9-]{1,38}[a-z0-9])\.git(/.*)?$`)

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m := gitPathRe.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.Error(w, "not a Cadence repository path: use /git/<project slug>.git", http.StatusNotFound)
		return
	}
	slug, rest := m[1], m[2]
	write := rest == "/git-receive-pack" || (rest == "/info/refs" && r.URL.Query().Get("service") == "git-receive-pack")
	if !allowedPath(rest, r) {
		http.Error(w, "only git's smart HTTP protocol is served here", http.StatusNotFound)
		return
	}
	access, err := h.authorize(r.Context(), slug, tokenOf(r), write)
	switch {
	case errors.Is(err, ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", `Basic realm="Cadence", charset="UTF-8"`)
		http.Error(w, "authenticate with an API key (cdk_…) or an agent session token (cst_…) as the password", http.StatusUnauthorized)
		return
	case errors.Is(err, ErrForbidden):
		http.Error(w, "this credential does not reach this project's repository", http.StatusForbidden)
		return
	case errors.Is(err, ErrNotFound):
		http.Error(w, "no such project repository", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "could not check the credential", http.StatusInternalServerError)
		return
	}
	if !h.s.Exists(slug) {
		http.Error(w, "no such project repository", http.StatusNotFound)
		return
	}
	if write && !access.Write {
		http.Error(w, "this credential may not push", http.StatusForbidden)
		return
	}
	env := []string{"GIT_PROJECT_ROOT=" + h.s.Root(), "GIT_HTTP_EXPORT_ALL=1"}
	for _, kv := range h.s.baseEnv() {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "GIT_CONFIG_") || strings.HasPrefix(kv, "LANG") ||
			strings.HasPrefix(kv, "LC_") {
			env = append(env, kv)
		}
	}
	if access.PushRefs != "" {
		env = append(env, "CADENCE_PUSH_REFS="+access.PushRefs)
	}
	if access.ReadOnly {
		env = append(env, "CADENCE_READ_ONLY=1")
	}
	user := access.User
	if user == "" {
		user = "cadence"
	}
	env = append(env, "REMOTE_USER="+user)
	backend := &cgi.Handler{
		Path:       h.s.git,
		Args:       []string{"http-backend"},
		Root:       HTTPPath,
		Env:        env,
		InheritEnv: []string{"PATH"},
	}
	if !(write && r.Method == http.MethodPost) {
		backend.ServeHTTP(w, r)
		return
	}
	// A push: hold the repository lock so the control plane's own commits do not interleave, then report moves.
	unlock := h.s.locked(slug)
	before, err := h.s.Refs(r.Context(), slug)
	if err != nil {
		unlock()
		http.Error(w, "could not read the repository", http.StatusInternalServerError)
		return
	}
	backend.ServeHTTP(w, r)
	after, err := h.s.Refs(context.WithoutCancel(r.Context()), slug)
	unlock()
	if err != nil || h.notify == nil {
		return
	}
	moved := map[string][2]string{}
	for name, sha := range after {
		if before[name] != sha {
			moved[name] = [2]string{before[name], sha}
		}
	}
	for name, sha := range before {
		if _, ok := after[name]; !ok {
			moved[name] = [2]string{sha, ""}
		}
	}
	if len(moved) > 0 {
		h.notify(context.WithoutCancel(r.Context()), slug, access, moved)
	}
}

// allowedPath admits the smart protocol's three endpoints and nothing of the dumb protocol (loose objects, packs).
func allowedPath(rest string, r *http.Request) bool {
	switch rest {
	case "/info/refs":
		svc := r.URL.Query().Get("service")
		return r.Method == http.MethodGet && (svc == "git-upload-pack" || svc == "git-receive-pack")
	case "/git-upload-pack", "/git-receive-pack":
		return r.Method == http.MethodPost
	}
	return false
}

// tokenOf returns the credential of a git request: Bearer, or the basic-auth password (git sends
// https://x-token:<token>@host/… that way).
func tokenOf(r *http.Request) string {
	h := r.Header.Get("Authorization")
	scheme, value, ok := strings.Cut(h, " ")
	if !ok {
		return ""
	}
	switch strings.ToLower(scheme) {
	case "bearer":
		return strings.TrimSpace(value)
	case "basic":
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
		if err != nil {
			return ""
		}
		_, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return ""
		}
		return pass
	}
	return ""
}
