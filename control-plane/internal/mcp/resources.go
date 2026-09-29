package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
)

// Resource URIs (docs/spec/05-agents.md, What the agent sees).
const (
	URIProjectSummary   = "project://summary"
	URIProjectTemplate  = "project://{p}/summary"
	URIDefaults         = "defaults://"
	URISelection        = "selection://current"
	URIHelpTemplate     = "help://{id}"
	resourceMIME        = "application/json"
	notAvailableYetNote = "not available yet"
)

// ErrNotAvailable is what a source answers while the part of Cadence behind it has not landed.
var ErrNotAvailable = errors.New(notAvailableYetNote)

// DefaultsSource provides the parsed defaults.yaml (R11) for defaults://. The registry stream implements it with
// defaults.Get(); until then the resource says the defaults are not available yet.
type DefaultsSource interface {
	Defaults(ctx context.Context) (any, error)
}

type placeholderDefaults struct{}

func (placeholderDefaults) Defaults(context.Context) (any, error) { return nil, ErrNotAvailable }

// Reference is one entity the user attached to a prompt, as the Chat panel renders it: @run:123,
// @eval:45#he-IL/[56,1], @utterance:9f3c.
type Reference struct {
	Ref      string `json:"ref"`                // the textual form, e.g. @eval:45#he-IL/[56,1]
	Kind     string `json:"kind"`               // EntityKind (snake singular): run, eval, utterance…
	ID       string `json:"id"`                 // the entity id
	Fragment string `json:"fragment,omitempty"` // the part of the entity, e.g. he-IL/[56,1]
	Label    string `json:"label,omitempty"`    // what the UI showed, for the agent's reply
}

// Selection is what the user attached for the agent to look at.
type Selection struct {
	References []Reference `json:"references"`
	UpdatedAt  *time.Time  `json:"updatedAt,omitempty"`
}

// SelectionStore returns the references attached for the caller: for an agent actor, its session's selection
// (actor.SessionID); for a user, their own. The Chat panel and agent sessions (phase 1, wave 2) implement it; until
// then the selection is empty.
type SelectionStore interface {
	Current(ctx context.Context, actor auth.Actor) (Selection, error)
}

type emptySelection struct{}

func (emptySelection) Current(context.Context, auth.Actor) (Selection, error) {
	return Selection{References: []Reference{}}, ErrNotAvailable
}

func (s *Server) addResources() {
	s.sdk.AddResource(&sdk.Resource{
		URI: URIProjectSummary, Name: "project-summary", Title: "Project summary", MIMEType: resourceMIME,
		Description: "The project this connection works on (Cadence-Project header or the token's project): locales, " +
			"base model, aliases, budgets and today's use, open approvals.",
	}, s.readProjectSummary)
	s.sdk.AddResourceTemplate(&sdk.ResourceTemplate{
		URITemplate: URIProjectTemplate, Name: "project-summary-by-slug", Title: "Project summary (by slug)",
		MIMEType: resourceMIME, Description: "The summary of project {p} (its slug); same shape as project://summary.",
	}, s.readProjectSummary)
	s.sdk.AddResource(&sdk.Resource{
		URI: URISelection, Name: "selection", Title: "Current selection", MIMEType: resourceMIME,
		Description: "The references the user attached to the conversation (@run:123, @eval:45#he-IL/[56,1], …).",
	}, s.readSelection)
	s.sdk.AddResource(&sdk.Resource{
		URI: URIDefaults, Name: "defaults", Title: "Defaults", MIMEType: resourceMIME,
		Description: "Every parameter default Cadence applies (defaults.yaml), with its source and safe range.",
	}, s.readDefaults)
	s.sdk.AddResourceTemplate(&sdk.ResourceTemplate{
		URITemplate: URIHelpTemplate, Name: "help", Title: "Help article", MIMEType: resourceMIME,
		Description: "A help article by id <section>.<slug>, e.g. help://errors.not-found; help.search finds ids.",
	}, s.readHelp)
}

func resourceResult(uri string, e envelope) *sdk.ReadResourceResult {
	e.Resource = uri
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: resourceMIME, Text: e.text()}}}
}

func notYet(why string) map[string]any {
	return map[string]any{"available": false, "note": notAvailableYetNote + ": " + why}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]string{"error": err.Error()})
	}
	return b
}

// readHelp serves help://<section>.<slug> through help.get.
func (s *Server) readHelp(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	uri := req.Params.URI
	id := strings.TrimPrefix(uri, "help://")
	resp := s.do(ctx, http.MethodGet, "/help/"+url.PathEscape(id), nil, forwardedHeader(extraHeader(req.Extra)), nil)
	switch {
	case resp.Status == http.StatusNotFound:
		return nil, sdk.ResourceNotFoundError(uri)
	case resp.Status >= 400:
		return resourceResult(uri, envelope{Status: resp.Status, Error: asJSON(resp.Body), Help: helpHint(resp.Body)}), nil
	}
	return resourceResult(uri, envelope{Status: resp.Status, Data: asJSON(resp.Body)}), nil
}

// readDefaults serves defaults://.
func (s *Server) readDefaults(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	d, err := s.opts.Defaults.Defaults(ctx)
	switch {
	case errors.Is(err, ErrNotAvailable):
		return resourceResult(req.Params.URI, envelope{Data: mustJSON(notYet(
			"defaults.yaml and defaults.get arrive with the registry core (phase 1, stream B); until then every " +
				"operation applies the defaults documented in its schema"))}), nil
	case err != nil:
		return nil, err
	}
	return resourceResult(req.Params.URI, envelope{Data: mustJSON(d)}), nil
}

// readSelection serves selection://current for the calling actor.
func (s *Server) readSelection(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	resp := s.do(ctx, http.MethodGet, "/me", nil, forwardedHeader(extraHeader(req.Extra)), nil)
	if resp.Status != http.StatusOK {
		return resourceResult(req.Params.URI, envelope{Status: resp.Status, Error: asJSON(resp.Body), Help: helpHint(resp.Body)}), nil
	}
	var actor auth.Actor
	if err := json.Unmarshal(resp.Body, &actor); err != nil {
		return nil, err
	}
	sel, err := s.opts.Selection.Current(ctx, actor)
	if sel.References == nil {
		sel.References = []Reference{}
	}
	out := map[string]any{"references": sel.References}
	if sel.UpdatedAt != nil {
		out["updatedAt"] = sel.UpdatedAt
	}
	switch {
	case errors.Is(err, ErrNotAvailable):
		out["note"] = notAvailableYetNote + ": references attached in the Chat panel arrive with agent sessions (phase 1, wave 2)"
	case err != nil:
		return nil, err
	}
	return resourceResult(req.Params.URI, envelope{Data: mustJSON(out)}), nil
}

// readProjectSummary serves project://summary (the connection's project) and project://{p}/summary.
func (s *Server) readProjectSummary(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	uri := req.Params.URI
	header := forwardedHeader(extraHeader(req.Extra))
	slug := header.Get(HeaderProject)
	if uri != URIProjectSummary {
		u, err := url.Parse(uri)
		if err != nil || u.Path != "/summary" || u.Host == "" {
			return nil, sdk.ResourceNotFoundError(uri)
		}
		slug = u.Host
	}
	if slug == "" {
		resp := s.do(ctx, http.MethodGet, "/projects", nil, header, nil)
		if resp.Status != http.StatusOK {
			return resourceResult(uri, envelope{Status: resp.Status, Error: asJSON(resp.Body), Help: helpHint(resp.Body)}), nil
		}
		return resourceResult(uri, envelope{
			Data: mustJSON(map[string]any{"project": nil, "projects": json.RawMessage(resp.Body)}),
			Next: "No project is bound to this connection (no " + HeaderProject + " header or project-bound token). " +
				"Read project://<slug>/summary for one of the projects listed here.",
		}), nil
	}
	resp := s.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(slug), nil, header, nil)
	if resp.Status == http.StatusNotFound {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	if resp.Status != http.StatusOK {
		return resourceResult(uri, envelope{Status: resp.Status, Error: asJSON(resp.Body), Help: helpHint(resp.Body)}), nil
	}
	summary := map[string]any{
		"project":   json.RawMessage(resp.Body),
		"locales":   notYet("locales come from project.yaml, written by the project wizard (phase 1, wave 2)"),
		"baseModel": notYet("base models arrive with the registry core (stream B) and the project wizard (wave 2)"),
		"aliases": s.section(ctx, header, "aliases.list", map[string]any{"p": slug, "project": slug},
			"aliases arrive with the registry core (phase 1, stream B)"),
		"budgets":  notYet("turn, token and GPU-hour budgets arrive with agent sessions (phase 1, wave 2)"),
		"todayUse": notYet("today's use against the budgets arrives with agent sessions (phase 1, wave 2)"),
		"openApprovals": s.section(ctx, header, "approvals.list", map[string]any{"p": slug, "project": slug},
			"approvals arrive with the policy engine (phase 1, stream C)"),
	}
	return resourceResult(uri, envelope{Data: mustJSON(summary)}), nil
}

// section fills one part of a summary by calling an operation when the manifest has it (it is implemented), with
// the arguments its input schema accepts; otherwise the part is marked not available yet.
func (s *Server) section(ctx context.Context, header http.Header, op string, args map[string]any, why string) any {
	t, ok := s.tools[op]
	if !ok {
		return notYet(why)
	}
	props, _ := t.InputSchema["properties"].(map[string]any)
	accepted := map[string]any{}
	for k, v := range args {
		if _, ok := props[k]; ok {
			accepted[k] = v
		}
	}
	call, err := buildCall(t.Tool, accepted)
	if err != nil {
		return map[string]any{"available": false, "note": err.Error()}
	}
	resp := s.do(ctx, t.Method, call.path, call.query, header, nil)
	if resp.Status != http.StatusOK {
		return map[string]any{"available": false, "error": asJSON(resp.Body)}
	}
	return asJSON(resp.Body)
}
