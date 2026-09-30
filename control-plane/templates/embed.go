// Package templates embeds the bootstrap templates tree (instructions, agent config, pipelines, skills and the
// permission presets). At start the control plane registers each unit as a registry template version; the
// bootstrap job renders them into project repositories.
package templates

import "embed"

// FS is the templates tree. Every top-level entry is embedded recursively, so a new directory (presets/) needs
// no change here; this file and README.md are embedded too and ignored by the registry.
//
//go:embed *
var FS embed.FS
