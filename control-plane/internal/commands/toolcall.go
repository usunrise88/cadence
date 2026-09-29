package commands

import "context"

// HeaderToolCallID carries the id of the agent tool call a request serves. The MCP server sets it on every call it
// forwards to the API (the client's tool-use id when it sends one, else the JSON-RPC request id); the pipeline
// records it as causedBy.toolCallId on the events the command emits.
const HeaderToolCallID = "Cadence-Tool-Call-Id"

// maxToolCallID bounds the header value kept for attribution; longer values are ignored.
const maxToolCallID = 200

type toolCallKey struct{}

// WithToolCallID returns ctx carrying the tool call id.
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallKey{}, id)
}

// ToolCallID returns the tool call id of the request, or "" when it does not come from an agent tool call.
func ToolCallID(ctx context.Context) string {
	id, _ := ctx.Value(toolCallKey{}).(string)
	return id
}
