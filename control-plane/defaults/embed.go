// Package defaults holds defaults.yaml, the one file every default lives in (docs/spec/08-resolutions.md R11),
// embedded in the binary. internal/defaults parses it; the worker image packages the same file.
package defaults

import _ "embed" // defaults.yaml

// YAML is the content of defaults.yaml.
//
//go:embed defaults.yaml
var YAML []byte
