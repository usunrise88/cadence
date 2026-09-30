// Package templates embeds the bootstrap templates the control plane renders (see README.md). Only the permission
// presets are embedded so far; the project bootstrap (phase 1, wave 2) adds the rest.
package templates

import "embed"

// Presets holds presets/<name>.yaml (docs/spec/08-resolutions.md R7).
//
//go:embed presets/*.yaml
var Presets embed.FS
