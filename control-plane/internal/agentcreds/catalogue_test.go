package agentcreds

import (
	"slices"
	"strings"
	"testing"
)

func TestMask(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sk-ant-oat01-abcdefghijklmnop1234", "…1234"},
		{"  sk-or-v1-0123456789abcdWXYZ \n", "…WXYZ"},
		{"short-key", ""}, // under 12 characters: nothing shown
		{"", ""},
		{"пароль-длинный-ключ-ёжик", "…ёжик"},
	}
	for _, c := range cases {
		if got := Mask(c.in); got != c.want {
			t.Errorf("Mask(%q) = %q, want %q", c.in, got, c.want)
		}
		if c.in != "" && strings.Contains(Mask(c.in), strings.TrimSpace(c.in)) {
			t.Errorf("Mask(%q) shows the whole value", c.in)
		}
	}
}

func TestParseID(t *testing.T) {
	cases := []struct {
		id, agent, provider string
		ok                  bool
	}{
		{"claude-code", AgentClaude, "anthropic", true},
		{"opencode.minimax", AgentOpencode, "minimax", true},
		{"opencode.vllm-lab", AgentOpencode, "vllm-lab", true},
		{"opencode.", "", "", false},
		{"opencode.VLLM", "", "", false},
		{"opencode.1abc", "", "", false},
		{"claude", "", "", false},
		{"opencode.a/b", "", "", false},
	}
	for _, c := range cases {
		agent, provider, ok := ParseID(c.id)
		if agent != c.agent || provider != c.provider || ok != c.ok {
			t.Errorf("ParseID(%q) = %q, %q, %v; want %q, %q, %v", c.id, agent, provider, ok, c.agent, c.provider, c.ok)
		}
		if ok && ID(agent, provider) != c.id {
			t.Errorf("ID(%q, %q) = %q, want %q", agent, provider, ID(agent, provider), c.id)
		}
	}
}

func TestCatalogue(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Catalogue {
		if seen[p.ID] {
			t.Errorf("duplicate catalogue id %s", p.ID)
		}
		seen[p.ID] = true
		if p.Agent != AgentClaude && p.Agent != AgentOpencode {
			t.Errorf("%s: unknown agent %q", p.ID, p.Agent)
		}
		if !p.Custom && len(p.Hosts) == 0 {
			t.Errorf("%s: a catalogue provider needs its API hosts for the egress allowlist", p.ID)
		}
		if p.Agent == AgentOpencode && !p.Custom && !strings.HasPrefix(p.VerifyModel, p.ID+"/") {
			t.Errorf("%s: verify model %q is not one of its own", p.ID, p.VerifyModel)
		}
	}
	for _, id := range []string{"claude-subscription", "minimax", "anthropic", "openai", "openrouter", "deepseek", "openai-compatible"} {
		if !seen[id] {
			t.Errorf("the catalogue lacks %s", id)
		}
	}
	if p, _ := Lookup(CatalogueMiniMax); p.DefaultModel != "minimax/MiniMax-M3" {
		t.Errorf("MiniMax suggests %q, want minimax/MiniMax-M3 (R6)", p.DefaultModel)
	}
	if got := CatalogueFor(AgentClaude, "anthropic").ID; got != CatalogueClaude {
		t.Errorf("Claude Code's entry is %s", got)
	}
	if got := CatalogueFor(AgentOpencode, "deepseek").ID; got != "deepseek" {
		t.Errorf("opencode.deepseek's entry is %s", got)
	}
	if got := CatalogueFor(AgentOpencode, "vllm").ID; got != CatalogueCustom {
		t.Errorf("opencode.vllm's entry is %s, want the custom one", got)
	}
	if got := CatalogueFor(AgentOpencode, "openai-compatible").ID; got != CatalogueCustom {
		t.Errorf("opencode.openai-compatible's entry is %s", got)
	}
}

func TestCheckValue(t *testing.T) {
	claude, _ := Lookup(CatalogueClaude)
	custom, _ := Lookup(CatalogueCustom)
	cases := []struct {
		p     Provider
		value string
		ok    bool
	}{
		{claude, "sk-ant-oat01-" + strings.Repeat("a", 40), true},
		{claude, "  sk-ant-oat01-" + strings.Repeat("a", 40) + "\n", true},
		{claude, "sk-proj-" + strings.Repeat("a", 40), false},
		{claude, "sk-ant-oat01 with space", false},
		{custom, "anything-goes-here", true},
		{custom, "short", false},
		{custom, "", false},
	}
	for _, c := range cases {
		err := CheckValue(c.p, c.value)
		if (err == nil) != c.ok {
			t.Errorf("CheckValue(%s, %q) = %v, want ok=%v", c.p.ID, c.value, err, c.ok)
		}
		if err != nil && len(c.value) > 12 && strings.Contains(err.Error(), strings.TrimSpace(c.value)) {
			t.Errorf("the error echoes the value: %v", err)
		}
	}
}

func TestBaseURLAndAllowlist(t *testing.T) {
	good := []struct{ in, host string }{
		{"http://vllm.lan:8000/v1", "vllm.lan:8000"},
		{"https://llm.example.com/v1", "llm.example.com"},
		{"https://llm.example.com:443/v1", "llm.example.com"},
		{"http://10.0.0.5:8000/v1", "10.0.0.5:8000"},
		{"http://[fd00::5]:8000/v1", "[fd00::5]:8000"},
		{"HTTP://VLLM.LAN/v1", "vllm.lan"},
	}
	for _, c := range good {
		u, err := ParseBaseURL(c.in)
		if err != nil {
			t.Errorf("ParseBaseURL(%q): %v", c.in, err)
			continue
		}
		if got := URLHost(u); got != c.host {
			t.Errorf("URLHost(%q) = %q, want %q", c.in, got, c.host)
		}
	}
	for _, in := range []string{"ftp://x/v1", "vllm.lan:8000", "http:///v1", "http://user:pw@vllm/v1", "http://vllm/v1?x=1", "not a url"} {
		if _, err := ParseBaseURL(in); err == nil {
			t.Errorf("ParseBaseURL(%q) accepted", in)
		}
	}
	got := Allowlist(
		HostsOf("claude-subscription", ""),
		HostsOf("minimax", ""),
		HostsOf("anthropic", ""),
		HostsOf("openai-compatible", "http://vllm.lan:8000/v1"),
		HostsOf("openai-compatible", "not a url"),
	)
	want := []string{"api.anthropic.com", "api.minimax.io", "vllm.lan:8000"}
	if !slices.Equal(got, want) {
		t.Errorf("Allowlist = %v, want %v", got, want)
	}
	if got := Allowlist(); got == nil || len(got) != 0 {
		t.Errorf("an empty allowlist is %v, want []", got)
	}
}
