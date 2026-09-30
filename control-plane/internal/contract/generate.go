package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Tool is one entry of the MCP tool manifest (internal/mcp/tools.json).
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	InputSchema map[string]any  `json:"inputSchema"`
	Annotations ToolAnnotations `json:"annotations"`
	// Params maps input properties back to where they go in the HTTP request.
	Params []ToolParam `json:"params"`
}

// ToolAnnotations are the MCP tool hints.
type ToolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
}

// ToolParam maps one input property to its place in the request.
type ToolParam struct {
	Property string `json:"property"`
	In       string `json:"in"` // path | query | header | body
	Name     string `json:"name"`
}

// Tools derives the MCP manifest: every implemented operation outside the exempt tags.
func (c *Contract) Tools() []Tool {
	var tools []Tool
	for _, o := range c.Ops {
		if o.Exempt || o.Planned > 0 {
			continue
		}
		props := map[string]any{}
		var required []string
		var params []ToolParam
		for _, p := range o.params() {
			if p.In == "header" && !strings.EqualFold(p.Name, "If-Match") {
				continue // Idempotency-Key is minted by the MCP server; other headers are transport details
			}
			prop := p.Name
			if p.In == "header" {
				prop = "ifMatch"
			}
			s := jsonSchema(p.Schema)
			if p.Description != "" {
				s["description"] = p.Description
			}
			props[prop] = s
			if p.Required {
				required = append(required, prop)
			}
			params = append(params, ToolParam{Property: prop, In: p.In, Name: p.Name})
		}
		if rb := o.op.RequestBody; rb != nil && rb.Value != nil {
			if mt := rb.Value.Content.Get("application/json"); mt != nil {
				props["body"] = jsonSchema(mt.Schema)
				if rb.Value.Required {
					required = append(required, "body")
				}
				params = append(params, ToolParam{Property: "body", In: "body", Name: "body"})
			}
		}
		sort.Strings(required)
		schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		desc := strings.TrimSpace(o.ToolDesc)
		if desc == "" {
			desc = o.Summary
		}
		if o.Mutation {
			if !strings.HasSuffix(desc, ".") {
				desc += "."
			}
			desc += " Pass dryRun=true to see what would happen without changing anything."
		}
		v := c.Vocab.Verbs[o.Verb] // from the vocabulary, not from Check: generation must not depend on call order
		tools = append(tools, Tool{
			Name: o.ID, Title: o.Summary, Description: desc, Method: o.Method, Path: o.Path,
			InputSchema: schema, Params: params,
			Annotations: ToolAnnotations{
				ReadOnlyHint:    !o.Mutation,
				DestructiveHint: o.Mutation && v.Reversible != nil && !*v.Reversible,
				IdempotentHint:  !o.Mutation || o.Method == "PUT",
			},
		})
	}
	return tools
}

// ToolsJSON renders the manifest deterministically.
func (c *Contract) ToolsJSON() ([]byte, error) {
	b, err := json.MarshalIndent(map[string]any{
		"generatedFrom": "api/openapi.yaml",
		"version":       c.Doc.Info.Version,
		"tools":         c.Tools(),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// jsonSchema inlines a schema reference into plain JSON Schema (no $ref), which is what MCP clients expect.
func jsonSchema(ref *openapi3.SchemaRef) map[string]any {
	if ref == nil || ref.Value == nil {
		return map[string]any{}
	}
	s := ref.Value
	raw, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"properties", "items", "additionalProperties", "allOf", "anyOf", "oneOf", "not", "example"} {
		delete(m, k)
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for name, p := range s.Properties {
			props[name] = jsonSchema(p)
		}
		m["properties"] = props
	}
	if s.Items != nil {
		m["items"] = jsonSchema(s.Items)
	}
	if s.AdditionalProperties.Has != nil {
		m["additionalProperties"] = *s.AdditionalProperties.Has
	}
	if s.AdditionalProperties.Schema != nil {
		m["additionalProperties"] = jsonSchema(s.AdditionalProperties.Schema)
	}
	for k, list := range map[string]openapi3.SchemaRefs{"allOf": s.AllOf, "anyOf": s.AnyOf, "oneOf": s.OneOf} {
		if len(list) == 0 {
			continue
		}
		var out []any
		for _, x := range list {
			out = append(out, jsonSchema(x))
		}
		m[k] = out
	}
	return m
}

// GoName is the identifier oapi-codegen gives an operation: jobs.get → JobsGet.
func GoName(opID string) string {
	var b strings.Builder
	for _, part := range strings.Split(opID, ".") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

// PlannedGo renders a Go type answering 501 for every planned operation, so the strict server compiles while
// later phases fill the operations in.
func (c *Contract) PlannedGo() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("// Code generated by control-plane/cmd/mcpgen from api/openapi.yaml. DO NOT EDIT.\n\n")
	b.WriteString("package api\n\nimport (\n\t\"context\"\n\t\"strconv\"\n)\n\n")
	b.WriteString("// Planned answers 501 for operations named in the contract but implemented in a later phase.\n")
	b.WriteString("// Embed it in the server; implementing an operation shadows its method here.\n")
	b.WriteString("type Planned struct{}\n\n")
	b.WriteString("// PlannedOperations maps each planned operationId to the roadmap phase that implements it.\n")
	b.WriteString("var PlannedOperations = map[string]int{\n")
	for _, o := range c.Ops {
		if o.Planned > 0 {
			fmt.Fprintf(&b, "\t%q: %d,\n", o.ID, o.Planned)
		}
	}
	b.WriteString("}\n\n")
	b.WriteString("func plannedProblem(opID string, phase int) Problem {\n")
	b.WriteString("\tdetail := opID + \" is planned for roadmap phase \" + strconv.Itoa(phase) + \" and not implemented yet\"\n")
	b.WriteString("\treturn Problem{Type: \"https://cadence.local/help/errors/not-implemented\", Title: \"Not implemented\", Status: 501, Detail: &detail}\n}\n\n")
	for _, o := range c.Ops {
		if o.Planned == 0 {
			continue
		}
		n := GoName(o.ID)
		fmt.Fprintf(&b, "// %s answers 501 until phase %d.\n", n, o.Planned)
		fmt.Fprintf(&b, "func (Planned) %s(_ context.Context, _ %sRequestObject) (%sResponseObject, error) {\n", n, n, n)
		fmt.Fprintf(&b, "\treturn %sdefaultApplicationProblemPlusJSONResponse{Body: plannedProblem(%q, %d), StatusCode: 501}, nil\n}\n\n", n, o.ID, o.Planned)
	}
	return format.Source(b.Bytes())
}

// OperationsTS renders the operation table and the vocabulary for the web command registry.
func (c *Contract) OperationsTS() []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by control-plane/cmd/mcpgen from api/openapi.yaml and api/vocabulary.yaml. DO NOT EDIT.\n\n")
	b.WriteString("export type VerbClass = \"read\" | \"mutate\";\n")
	b.WriteString("export type Confirm = \"none\" | \"inline\" | \"approval\" | \"modal\";\n\n")
	b.WriteString("export type VerbSpec = {\n  readonly class: VerbClass;\n  readonly reversible: boolean | null;\n  readonly confirm: Confirm;\n  readonly icon: string;\n  readonly key?: string;\n  readonly agentOnly?: boolean;\n};\n\n")
	names := make([]string, 0, len(c.Vocab.Verbs))
	for n := range c.Vocab.Verbs {
		names = append(names, n)
	}
	sort.Strings(names)
	b.WriteString("export const verbs = {\n")
	for _, n := range names {
		v := c.Vocab.Verbs[n]
		rev := "null"
		if v.Reversible != nil {
			rev = fmt.Sprint(*v.Reversible)
		}
		fmt.Fprintf(&b, "  %s: { class: %q, reversible: %s, confirm: %q, icon: %q", n, v.Class, rev, v.Confirm, v.Icon)
		if v.Key != "" {
			fmt.Fprintf(&b, ", key: %q", v.Key)
		}
		if v.AgentOnly {
			b.WriteString(", agentOnly: true")
		}
		b.WriteString(" },\n")
	}
	b.WriteString("} as const satisfies Record<string, VerbSpec>;\n\nexport type Verb = keyof typeof verbs;\n\n")
	b.WriteString("export type OperationSpec = {\n  readonly id: string;\n  readonly entity: string;\n  readonly verb: string;\n  readonly kind: string;\n  readonly method: \"GET\" | \"POST\" | \"PUT\" | \"PATCH\";\n  readonly path: string;\n  readonly summary: string;\n  readonly planned: number;\n  readonly mcp: boolean;\n};\n\n")
	b.WriteString("export const operations = {\n")
	for _, o := range c.Ops {
		fmt.Fprintf(&b, "  %q: { id: %q, entity: %q, verb: %q, kind: %q, method: %q, path: %q, summary: %q, planned: %d, mcp: %t },\n",
			o.ID, o.ID, o.Entity, o.Verb, o.Kind, o.Method, o.Path, o.Summary, o.Planned, !o.Exempt && o.Planned == 0)
	}
	b.WriteString("} as const satisfies Record<string, OperationSpec>;\n\nexport type OperationId = keyof typeof operations;\n")
	return b.Bytes()
}
