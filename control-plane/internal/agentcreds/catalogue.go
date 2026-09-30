package agentcreds

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Agents, as in the contract's AgentDriver.
const (
	AgentClaude   = "claude-code"
	AgentOpencode = "opencode"
)

// Catalogue ids with a meaning of their own.
const (
	CatalogueClaude  = "claude-subscription"
	CatalogueCustom  = "openai-compatible"
	CatalogueMiniMax = "minimax"
)

// ClaudeID is the id of the Claude Code subscription credential.
const ClaudeID = AgentClaude

// ClaudeTokenLifetime is how long a `claude setup-token` token is expected to last (about a year); the UI warns 30
// days before (docs/spec/07-audit-risks-sources.md "Open questions": the exact lifetime is Anthropic's).
const ClaudeTokenLifetime = 365 * 24 * time.Hour

// Provider is one catalogue entry (the contract's AgentProvider).
type Provider struct {
	ID          string   `json:"id"`
	Agent       string   `json:"agent"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	KeyLabel    string   `json:"keyLabel"`
	KeyPrefix   string   `json:"keyPrefix,omitempty"`
	KeyRequired bool     `json:"keyRequired"`
	Custom      bool     `json:"custom"`
	Hosts       []string `json:"hosts"`
	// DefaultModel is suggested as opencode's default once the provider is configured.
	DefaultModel string `json:"defaultModel,omitempty"`
	// VerifyModel is the cheap model of the verification request (Claude alias or opencode provider/model).
	VerifyModel string `json:"verifyModel,omitempty"`
	Help        string `json:"help,omitempty"`
	DocsURL     string `json:"docsUrl,omitempty"`
}

// Catalogue is every provider the Agents settings offer: Claude Code's subscription and opencode's providers
// (opencode's own provider ids, so `opencode models <id>` and provider/model ids line up). Hosts are what the
// egress proxy allows once a credential is configured (R4).
var Catalogue = []Provider{
	{
		ID: CatalogueClaude, Agent: AgentClaude, Name: "Claude subscription",
		Description: "Claude Code sessions run on the owner's Claude subscription (R6) with a long-lived token.",
		KeyLabel:    "Token from claude setup-token", KeyPrefix: "sk-ant-", KeyRequired: true,
		Hosts: []string{"api.anthropic.com"}, VerifyModel: "haiku",
		Help: "Run `claude setup-token` once on any machine with a browser, sign in with the Claude account and paste " +
			"the sk-ant-oat… token it prints. It lasts about a year.",
		DocsURL: "https://docs.claude.com/en/docs/claude-code/setup",
	},
	{
		ID: CatalogueMiniMax, Agent: AgentOpencode, Name: "MiniMax",
		Description: "MiniMax through its Token Plan (R6); the owner's choice for opencode sessions.",
		KeyLabel:    "MiniMax API key", KeyRequired: true, Hosts: []string{"api.minimax.io"},
		DefaultModel: "minimax/MiniMax-M3", VerifyModel: "minimax/MiniMax-M3",
		Help:    "Create a key on platform.minimax.io (API keys) for the Token Plan.",
		DocsURL: "https://platform.minimax.io",
	},
	{
		ID: "anthropic", Agent: AgentOpencode, Name: "Anthropic API",
		Description: "Claude models through the Anthropic API (billed per token).",
		KeyLabel:    "Anthropic API key", KeyPrefix: "sk-ant-", KeyRequired: true, Hosts: []string{"api.anthropic.com"},
		VerifyModel: "anthropic/claude-haiku-4-5", Help: "Create a key in the Claude Console (Settings → API keys).",
		DocsURL: "https://console.anthropic.com",
	},
	{
		ID: "openai", Agent: AgentOpencode, Name: "OpenAI",
		Description: "OpenAI models through the OpenAI API.",
		KeyLabel:    "OpenAI API key", KeyPrefix: "sk-", KeyRequired: true, Hosts: []string{"api.openai.com"},
		VerifyModel: "openai/gpt-5-nano", Help: "Create a key on platform.openai.com (API keys).",
		DocsURL: "https://platform.openai.com",
	},
	{
		ID: "openrouter", Agent: AgentOpencode, Name: "OpenRouter",
		Description: "Many providers' models through one OpenRouter key.",
		KeyLabel:    "OpenRouter API key", KeyPrefix: "sk-or-", KeyRequired: true, Hosts: []string{"openrouter.ai"},
		VerifyModel: "openrouter/anthropic/claude-haiku-4.5", Help: "Create a key on openrouter.ai (Keys).",
		DocsURL: "https://openrouter.ai",
	},
	{
		ID: "deepseek", Agent: AgentOpencode, Name: "DeepSeek",
		Description: "DeepSeek models through the DeepSeek API.",
		KeyLabel:    "DeepSeek API key", KeyPrefix: "sk-", KeyRequired: true, Hosts: []string{"api.deepseek.com"},
		VerifyModel: "deepseek/deepseek-v4-flash", Help: "Create a key on platform.deepseek.com (API keys).",
		DocsURL: "https://platform.deepseek.com",
	},
	{
		ID: CatalogueCustom, Agent: AgentOpencode, Name: "OpenAI-compatible (custom base URL)",
		Description: "Any OpenAI-compatible server, e.g. a self-hosted vLLM; its models are read from <base URL>/models.",
		KeyLabel:    "API key (optional)", KeyRequired: false, Custom: true, Hosts: []string{},
		Help: "Name the provider (its id prefixes the models, e.g. vllm/…), give the base URL including /v1, and a key " +
			"if the server wants one.",
	},
}

// Lookup returns the catalogue entry with id.
func Lookup(id string) (Provider, bool) {
	for _, p := range Catalogue {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

var (
	idRe = regexp.MustCompile(`^(claude-code|opencode\.([a-z][a-z0-9-]{0,39}))$`)
)

// ParseID splits a credential id into its agent and provider: claude-code → (claude-code, anthropic);
// opencode.<p> → (opencode, p).
func ParseID(id string) (agent, provider string, ok bool) {
	m := idRe.FindStringSubmatch(id)
	if m == nil {
		return "", "", false
	}
	if m[1] == AgentClaude {
		return AgentClaude, "anthropic", true
	}
	return AgentOpencode, m[2], true
}

// ID is the credential id of agent's provider.
func ID(agent, provider string) string {
	if agent == AgentClaude {
		return ClaudeID
	}
	return AgentOpencode + "." + provider
}

// CatalogueFor returns the catalogue entry of a credential: Claude's subscription, an opencode provider of the
// catalogue by its id, or the custom (OpenAI-compatible) entry for any other opencode provider id.
func CatalogueFor(agent, provider string) Provider {
	if agent == AgentClaude {
		p, _ := Lookup(CatalogueClaude)
		return p
	}
	if p, ok := Lookup(provider); ok && p.Agent == AgentOpencode && !p.Custom {
		return p
	}
	p, _ := Lookup(CatalogueCustom)
	return p
}

// Mask is the hint of a value: its last four characters behind an ellipsis, or nothing for a value too short to
// show any of it safely (under 12 characters).
func Mask(value string) string {
	v := strings.TrimSpace(value)
	if len([]rune(v)) < 12 {
		return ""
	}
	r := []rune(v)
	return "…" + string(r[len(r)-4:])
}

// CheckValue validates a submitted key or token for entry p: no white space inside, the provider's prefix when it
// has one. It never echoes the value.
func CheckValue(p Provider, value string) error {
	v := strings.TrimSpace(value)
	switch {
	case v == "":
		return fmt.Errorf("is empty")
	case strings.ContainsAny(v, " \t\r\n"):
		return fmt.Errorf("contains white space; paste the key alone")
	case p.KeyPrefix != "" && !strings.HasPrefix(v, p.KeyPrefix):
		return fmt.Errorf("does not look like a %s (it starts with %s…)", p.KeyLabel, p.KeyPrefix)
	case len(v) < 8:
		return fmt.Errorf("is too short for a key")
	}
	return nil
}

// ParseBaseURL checks a custom provider's base URL: http or https with a host, no credentials, query or fragment.
func ParseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil:
		return nil, fmt.Errorf("is not a URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("must be an http or https URL, e.g. http://vllm.lan:8000/v1")
	case u.Hostname() == "":
		return nil, fmt.Errorf("names no host")
	case u.User != nil:
		return nil, fmt.Errorf("must not carry credentials; put the key in the key field")
	case u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("must not have a query or fragment")
	}
	return u, nil
}

// URLHost is the allowlist entry of a base URL: the host name, or host:port when the URL names a port other than its
// scheme's default (the proxy's default ports are 443 and 80).
func URLHost(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return host
	}
	return net.JoinHostPort(host, port)
}

// HostsOf returns the egress hosts one credential needs: its catalogue entry's hosts, or a custom base URL's host.
func HostsOf(catalogueID, baseURL string) []string {
	p, ok := Lookup(catalogueID)
	if ok && !p.Custom {
		return slices.Clone(p.Hosts)
	}
	if u, err := ParseBaseURL(baseURL); err == nil {
		return []string{URLHost(u)}
	}
	return []string{}
}

// Allowlist merges the hosts of several credentials into one sorted list without duplicates.
func Allowlist(hosts ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, hs := range hosts {
		for _, h := range hs {
			if h != "" && !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	sort.Strings(out)
	return out
}
