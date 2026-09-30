package mcp

import (
	_ "embed" // tools.json is embedded
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/usunrise88/cadence/control-plane/internal/contract"
)

// manifestJSON is written by cmd/mcpgen from api/openapi.yaml (make gen); the binary serves exactly these tools.
//
//go:embed tools.json
var manifestJSON []byte

// Manifest is the parsed tools.json.
type Manifest struct {
	GeneratedFrom string          `json:"generatedFrom"`
	Version       string          `json:"version"`
	Tools         []contract.Tool `json:"tools"`
}

// tool is a manifest entry ready to serve: its input schema resolved for validation.
type tool struct {
	contract.Tool
	schema *jsonschema.Resolved
}

// LoadManifest parses the embedded manifest.
func LoadManifest() (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse embedded tools.json: %w", err)
	}
	return m, nil
}

// prepare resolves every tool's input schema; a schema the validator cannot resolve fails the start.
func prepare(m Manifest) ([]tool, error) {
	out := make([]tool, 0, len(m.Tools))
	for _, t := range m.Tools {
		raw, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s: marshal input schema: %w", t.Name, err)
		}
		var s jsonschema.Schema
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("tool %s: parse input schema: %w", t.Name, err)
		}
		resolved, err := s.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("tool %s: resolve input schema: %w", t.Name, err)
		}
		out = append(out, tool{Tool: t, schema: resolved})
	}
	return out, nil
}
