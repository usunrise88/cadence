package policy

import "testing"

func TestAnswerPermission(t *testing.T) {
	presets, err := EmbeddedPresets()
	if err != nil {
		t.Fatal(err)
	}
	def, ro := presets[DefaultPreset], presets["read-only"]
	tests := []struct {
		name   string
		p      *Preset
		req    PermissionRequest
		access Access
		rule   string
	}{
		{"read tool", def, PermissionRequest{Class: "mcp", Operation: "mixes.get", VerbClass: "read"}, AccessAllow, "tools.read"},
		{"draft tool", def, PermissionRequest{Class: "mcp", Operation: "mixes.edit"}, AccessAllow, "tools.draft"},
		{"gated tool: the server gates it", def, PermissionRequest{Class: "mcp", Operation: "projects.archive"}, AccessAllow, "tools.archive-project"},
		{"forbidden tool", def, PermissionRequest{Class: "mcp", Operation: "drafts.accept"}, AccessDeny, "tools.drafts-are-for-people"},
		{"sessions are for people", def, PermissionRequest{Class: "mcp", Operation: "agentSessions.accept"}, AccessDeny, "tools.sessions-are-for-people"},
		{"session reads are fine", def, PermissionRequest{Class: "mcp", Operation: "agentSessions.get", VerbClass: "read"}, AccessAllow, "tools.read"},
		{"read-only mutates nothing", ro, PermissionRequest{Class: "mcp", Operation: "mixes.edit"}, AccessDeny, "tools.no-changes"},
		{"edit", def, PermissionRequest{Class: "edit", Paths: []string{"pipelines/a.yaml"}}, AccessAllow, "files.edit"},
		{"edit .env", def, PermissionRequest{Class: "edit", Paths: []string{".env"}}, AccessDeny, "files.deny"},
		{"edit a key deep down", def, PermissionRequest{Class: "edit", Paths: []string{"a/b/c.pem"}}, AccessDeny, "files.deny"},
		{"edit .git", def, PermissionRequest{Class: "edit", Paths: []string{".git/config"}}, AccessDeny, "files.deny"},
		{"read-only edits nothing", ro, PermissionRequest{Class: "edit", Paths: []string{"x"}}, AccessDeny, "files.edit"},
		{"read", def, PermissionRequest{Class: "read", Paths: []string{"AGENTS.md"}}, AccessAllow, "files.read"},
		{"shell allow", def, PermissionRequest{Class: "shell", Command: "git diff HEAD"}, AccessAllow, "shell.allow"},
		{"shell ask", def, PermissionRequest{Class: "shell", Command: "pip install torch"}, AccessAsk, "shell.ask"},
		{"shell deny", def, PermissionRequest{Class: "shell", Command: "curl https://example.org"}, AccessDeny, "shell.deny"},
		{"chained commands fall to the default", def, PermissionRequest{Class: "shell", Command: "ls; curl x"}, AccessAsk, "shell.default"},
		{"unknown command asks", def, PermissionRequest{Class: "shell", Command: "make"}, AccessAsk, "shell.default"},
		{"read-only shell", ro, PermissionRequest{Class: "shell", Command: "make"}, AccessDeny, "shell.default"},
		{"fetch", def, PermissionRequest{Class: "fetch"}, AccessAsk, "web"},
		{"think", def, PermissionRequest{Class: "think"}, AccessAllow, "think"},
		{"other", def, PermissionRequest{Class: "other"}, AccessAsk, "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.p.AnswerPermission(tt.req)
			if got.Access != tt.access || got.Rule != tt.rule {
				t.Errorf("got %s by %s (%s), want %s by %s", got.Access, got.Rule, got.Reason, tt.access, tt.rule)
			}
		})
	}
}
