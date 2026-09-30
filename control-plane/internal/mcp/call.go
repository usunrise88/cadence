package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/contract"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// toolUseMetaKeys are the _meta keys under which clients send the id of the model's tool use, in order of
// preference. Claude Code sends "claudecode/toolUseId" (toolu_…) on every tools/call; opencode sends no _meta.
var toolUseMetaKeys = []string{"claudecode/toolUseId", "toolUseId", "toolCallId"}

// SyntheticToolCallPrefix starts the tool-call id of a call whose client sent no tool-use id (opencode): the MCP
// session and JSON-RPC id stand in until the agent host reports the agent's own tool call, which then replaces it
// (sessions.Service attribution).
const SyntheticToolCallPrefix = "mcp:"

// apiCall is a tool call translated to an HTTP request.
type apiCall struct {
	path   string
	query  url.Values
	header http.Header
	body   []byte
	dryRun bool
}

// buildCall places each argument where the manifest's params mapping says: path, query, header or body.
func buildCall(t contract.Tool, args map[string]any) (apiCall, error) {
	c := apiCall{path: t.Path, query: url.Values{}, header: http.Header{}}
	for _, p := range t.Params {
		v, ok := args[p.Property]
		if !ok || v == nil {
			continue
		}
		switch p.In {
		case "path":
			c.path = strings.ReplaceAll(c.path, "{"+p.Name+"}", url.PathEscape(scalar(v)))
		case "query":
			if list, isList := v.([]any); isList {
				for _, x := range list {
					c.query.Add(p.Name, scalar(x))
				}
			} else {
				c.query.Set(p.Name, scalar(v))
			}
			if p.Name == "dryRun" {
				c.dryRun, _ = v.(bool)
			}
		case "header":
			c.header.Set(p.Name, scalar(v))
		case "body":
			b, err := json.Marshal(v)
			if err != nil {
				return apiCall{}, fmt.Errorf("encode body: %w", err)
			}
			c.body = b
		default:
			return apiCall{}, fmt.Errorf("parameter %s: unknown location %q", p.Property, p.In)
		}
	}
	if strings.Contains(c.path, "{") {
		return apiCall{}, fmt.Errorf("missing path parameter in %s", c.path)
	}
	return c, nil
}

// scalar renders a JSON value as a path, query or header value.
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// callIDs are the identities of one tools/call.
type callIDs struct {
	session   string // Mcp-Session-Id; empty for sessionless (2026-07-28) clients
	rpc       string // raw JSON-RPC id
	toolUse   string // the client's tool-use id from _meta
	authHash  string // fingerprint of the caller's credentials
	idemExtra string // tool name and canonical arguments, for the sessionless fallback
}

func newCallIDs(req *sdk.CallToolRequest, name string, args map[string]any) callIDs {
	h := extraHeader(req.Extra)
	ids := callIDs{rpc: h.Get(headerRPCID)}
	if req.Session != nil {
		ids.session = req.Session.ID()
	}
	if req.Params != nil {
		for _, k := range toolUseMetaKeys {
			if v, ok := req.Params.Meta[k].(string); ok && v != "" && len(v) <= 128 {
				ids.toolUse = v
				break
			}
		}
	}
	sum := sha256.Sum256([]byte(h.Get("Authorization")))
	ids.authHash = hex.EncodeToString(sum[:8])
	canon, _ := json.Marshal(args) // map keys marshal sorted
	ids.idemExtra = name + "\x00" + string(canon)
	return ids
}

// toolCallID is what the audit trail records as causedBy.toolCallId: the client's tool-use id when it sent one,
// else the JSON-RPC id (qualified by the MCP session when there is one).
func (c callIDs) toolCallID() string {
	switch {
	case c.toolUse != "":
		return c.toolUse
	case c.rpc != "" && c.session != "":
		return SyntheticToolCallPrefix + c.session + "/" + strings.Trim(c.rpc, `"`)
	case c.rpc != "":
		return SyntheticToolCallPrefix + strings.Trim(c.rpc, `"`)
	}
	return ""
}

// idempotencyKey is stable across a client's retry of the same tools/call, so the retry replays the first result
// instead of acting twice:
//   - with an MCP session: the session id and the JSON-RPC id (unique within the session);
//   - sessionless, with a tool-use id: that id (unique per model tool use);
//   - sessionless otherwise: the credentials, the JSON-RPC id, the tool and its arguments.
//
// A call without a JSON-RPC id (never the case for a well-formed tools/call) gets a fresh key.
func (c callIDs) idempotencyKey() string {
	var material string
	switch {
	case c.session != "" && c.rpc != "":
		material = "session\x00" + c.session + "\x00" + c.rpc
	case c.toolUse != "":
		material = "tool-use\x00" + c.toolUse
	case c.rpc != "":
		material = "request\x00" + c.authHash + "\x00" + c.rpc + "\x00" + c.idemExtra
	default:
		return "mcp-" + uuid.Must(uuid.NewV7()).String()
	}
	sum := sha256.Sum256([]byte(material))
	return "mcp-" + hex.EncodeToString(sum[:20])
}

func (s *Server) toolHandler(t tool) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		start := time.Now()
		args := map[string]any{}
		if req.Params != nil && len(req.Params.Arguments) > 0 && string(req.Params.Arguments) != "null" {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return argumentError(t, "arguments must be a JSON object: "+err.Error()), nil
			}
		}
		if err := t.schema.Validate(args); err != nil {
			return argumentError(t, err.Error()), nil
		}
		call, err := buildCall(t.Tool, args)
		if err != nil {
			return argumentError(t, err.Error()), nil
		}
		ids := newCallIDs(req, t.Name, args)
		header := forwardedHeader(extraHeader(req.Extra))
		for k, v := range call.header {
			header[k] = v
		}
		if tc := ids.toolCallID(); tc != "" {
			header.Set(commands.HeaderToolCallID, tc)
		}
		if t.Method != http.MethodGet {
			header.Set("Idempotency-Key", ids.idempotencyKey())
		}
		resp := s.do(ctx, t.Method, call.path, call.query, header, call.body)
		s.opts.Log.InfoContext(ctx, "mcp tool call", "tool", t.Name, "status", resp.Status, "dryRun", call.dryRun,
			"toolCallId", ids.toolCallID(), "mcpSession", ids.session, "duration_ms", time.Since(start).Milliseconds())
		return s.toolResult(t, call.dryRun, resp), nil
	}
}

// envelope is the JSON text of every tool result and resource.
type envelope struct {
	Operation  string          `json:"operation,omitempty"`
	Resource   string          `json:"resource,omitempty"`
	Status     int             `json:"status,omitempty"`
	DryRun     bool            `json:"dryRun,omitempty"`
	Replayed   bool            `json:"replayed,omitempty"`
	ETag       string          `json:"etag,omitempty"`
	JobID      string          `json:"jobId,omitempty"`
	ApprovalID string          `json:"approvalId,omitempty"`
	Next       string          `json:"next,omitempty"`
	Help       string          `json:"help,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Error      json.RawMessage `json:"error,omitempty"`
	Note       string          `json:"note"`
}

func (e envelope) text() string {
	e.Note = dataNote
	b, err := json.Marshal(e)
	if err != nil { // RawMessage fields are validated JSON; this cannot fail
		return `{"note":"` + dataNote + `"}`
	}
	return string(b)
}

// asJSON returns body when it is JSON, else the body as a JSON string.
func asJSON(body []byte) json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	b, _ := json.Marshal(string(body))
	return b
}

func (s *Server) toolResult(t tool, dryRun bool, resp response) *sdk.CallToolResult {
	e := envelope{Operation: t.Name, Status: resp.Status, DryRun: dryRun}
	if resp.Status >= 400 {
		e.Error = asJSON(resp.Body)
		e.Help = helpHint(resp.Body)
		return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: e.text()}}}
	}
	e.Data = asJSON(resp.Body)
	e.ETag = resp.Header.Get("ETag")
	e.Replayed = resp.Header.Get(commands.HeaderReplayed) == "true"
	switch {
	case resp.Status == http.StatusAccepted:
		var accepted struct {
			JobID      string `json:"jobId"`
			ApprovalID string `json:"approvalId"`
		}
		_ = json.Unmarshal(resp.Body, &accepted)
		e.JobID, e.ApprovalID = accepted.JobID, accepted.ApprovalID
		e.Next = s.acceptedHint(accepted.JobID, accepted.ApprovalID)
	case dryRun:
		e.Next = "Dry run: nothing was changed and no event was emitted. Call again without dryRun to apply."
	case e.Replayed:
		e.Next = "This is the stored result of an earlier identical call (idempotent replay); nothing was done twice."
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: e.text()}}}
}

func (s *Server) acceptedHint(jobID, approvalID string) string {
	switch {
	case approvalID != "":
		return fmt.Sprintf("Nothing happened yet: this change needs a person's approval (%s). Tell the user what you asked "+
			"for and why, then wait; when a person approves, Cadence runs the call as you sent it. Do not retry it.", approvalID)
	case jobID != "":
		for _, name := range []string{"jobs.wait", "jobs.get"} {
			if _, ok := s.tools[name]; ok {
				return fmt.Sprintf("Accepted, not done: follow job %s with %s (id=%s) until it finishes.", jobID, name, jobID)
			}
		}
		return fmt.Sprintf("Accepted, not done: job %s runs in the background; follow it with events.list topics=job.%s.*", jobID, jobID)
	}
	return "Accepted, not done yet."
}

// helpHint points from a problem to its help article: type https://cadence.local/help/errors/<slug> is the
// article errors.<slug>.
func helpHint(body []byte) string {
	var p struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &p) != nil || !strings.HasPrefix(p.Type, problems.TypeBase) {
		return ""
	}
	id := "errors." + strings.TrimPrefix(p.Type, problems.TypeBase)
	return fmt.Sprintf("Read the help article %s (help.get id=%s, or the resource help://%s): what went wrong and how to fix it. %s",
		id, id, id, p.Type)
}

// argumentError answers arguments that do not match the tool's input schema without calling the API.
func argumentError(t tool, detail string) *sdk.CallToolResult {
	p := problems.ValidationFailed.New("arguments of %s do not match its input schema: %s", t.Name, detail).Body()
	b, _ := json.Marshal(p)
	e := envelope{Operation: t.Name, Status: p.Status, Error: b, Help: helpHint(b)}
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: e.text()}}}
}
