// Package mcptest connects the official Go SDK client to a Cadence MCP endpoint for tests: extra headers on every
// request (Authorization, Cadence-Project) and, on demand, a transport-level retry of every tools/call.
package mcptest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configure a test client.
type Options struct {
	Header http.Header
	// Retry sends every tools/call POST twice and returns the second answer, as a client does after a lost response.
	Retry bool
	// ProtocolVersion pins the protocol ("2025-11-25" has MCP sessions; the default latest one is sessionless).
	ProtocolVersion string
}

// Transport adds headers and duplicates tools/call requests.
type Transport struct {
	Base   http.RoundTripper
	Header http.Header
	Retry  bool

	mu        sync.Mutex
	toolCalls int // tools/call POSTs sent, retries included
}

// ToolCalls reports how many tools/call POSTs reached the server.
func (t *Transport) ToolCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.toolCalls
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.Header {
		req.Header[k] = v
	}
	if req.Method != http.MethodPost || req.Body == nil {
		return t.Base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	var msg struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &msg)
	send := func() (*http.Response, error) {
		r := req.Clone(req.Context())
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		return t.Base.RoundTrip(r)
	}
	if msg.Method != "tools/call" {
		return send()
	}
	t.mu.Lock()
	t.toolCalls++
	t.mu.Unlock()
	if t.Retry {
		first, err := send()
		if err != nil {
			return nil, err
		}
		_, _ = io.Copy(io.Discard, first.Body)
		_ = first.Body.Close()
		t.mu.Lock()
		t.toolCalls++
		t.mu.Unlock()
	}
	return send()
}

// Connect opens a client session to endpoint (…/mcp); it is closed when the test ends.
func Connect(t *testing.T, endpoint string, o Options) (*sdk.ClientSession, *Transport) {
	t.Helper()
	tr := &Transport{Base: http.DefaultTransport, Header: o.Header, Retry: o.Retry}
	client := sdk.NewClient(&sdk.Implementation{Name: "cadence-test", Version: "test"}, nil)
	var opts *sdk.ClientSessionOptions
	if o.ProtocolVersion != "" {
		opts = &sdk.ClientSessionOptions{ProtocolVersion: o.ProtocolVersion}
	}
	cs, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: &http.Client{Transport: tr}, DisableStandaloneSSE: true,
	}, opts)
	if err != nil {
		t.Fatalf("connect %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, tr
}

// Envelope is the JSON text of a Cadence tool result or resource.
type Envelope struct {
	Operation  string          `json:"operation"`
	Resource   string          `json:"resource"`
	Status     int             `json:"status"`
	DryRun     bool            `json:"dryRun"`
	Replayed   bool            `json:"replayed"`
	ETag       string          `json:"etag"`
	JobID      string          `json:"jobId"`
	ApprovalID string          `json:"approvalId"`
	Next       string          `json:"next"`
	Help       string          `json:"help"`
	Data       json.RawMessage `json:"data"`
	Error      json.RawMessage `json:"error"`
	Note       string          `json:"note"`
}

// Call calls a tool and decodes its envelope; isError is the result's IsError.
func Call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any, meta sdk.Meta) (Envelope, bool) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args, Meta: meta})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("call %s: %d content items, want 1", name, len(res.Content))
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("call %s: content is %T, want text", name, res.Content[0])
	}
	var e Envelope
	if err := json.Unmarshal([]byte(text.Text), &e); err != nil {
		t.Fatalf("call %s: result is not JSON: %v\n%s", name, err, text.Text)
	}
	if e.Note == "" {
		t.Fatalf("call %s: result is not marked as data: %s", name, text.Text)
	}
	return e, res.IsError
}

// Read reads a resource and decodes its envelope.
func Read(t *testing.T, cs *sdk.ClientSession, uri string) Envelope {
	t.Helper()
	res, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("read %s: %v", uri, err)
	}
	if len(res.Contents) != 1 || res.Contents[0].MIMEType != "application/json" {
		t.Fatalf("read %s: unexpected contents %+v", uri, res.Contents)
	}
	var e Envelope
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &e); err != nil {
		t.Fatalf("read %s: not JSON: %v", uri, err)
	}
	if e.Note == "" {
		t.Fatalf("read %s: not marked as data", uri)
	}
	return e
}
