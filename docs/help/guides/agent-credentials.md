---
title: Connecting the agents' model accounts
summary: Settings → Agents — the Claude Code subscription token from `claude setup-token`, opencode providers (MiniMax, Anthropic, OpenAI, OpenRouter, DeepSeek, a self-hosted vLLM), Verify, the default model and the egress allowlist.
contexts: [guide:agent-credentials, panel:settings]
---

## What this is

Claude Code and opencode sessions call their models with the agents' own accounts, not with Cadence's secrets. Those
accounts are configured once for the whole instance, by the admin, in **Settings → Agents**:

- **Claude Code** uses the owner's Claude subscription through a long-lived token from `claude setup-token`.
- **opencode** uses one or more providers from a catalogue — MiniMax (the Token Plan; default), Anthropic, OpenAI,
  OpenRouter, DeepSeek — or any **OpenAI-compatible** server at a base URL you give, such as a self-hosted vLLM.

A value you type is write-only. Cadence seals it in its encrypted secret store only until the agent host has picked it
up: the host writes it into the `agent-credentials` volume in the agent's own format (`claude/oauth-token`;
`opencode/auth.json`, plus a provider block in `opencode/opencode.json` for a custom base URL), acknowledges, and the
control plane deletes its copy. The database keeps only the last four characters, who set it and when, the expected
expiry, and the delivery and verification results. No response, event, log line, audit row or agent ever sees the
value; the operations are not MCP tools either.

## Place in the loop

Prepare. Do this once, before the first agent session; come back when a token expires, a key changes, or you add a
provider.

## Fields and defaults

| Field | Default | Source |
| --- | --- | --- |
| Claude verification model | `haiku` | Cadence recommendation (the cheapest alias) |
| Claude token expected expiry | set + 365 days, warning 30 days before | `claude setup-token` issues a token valid for about a year |
| opencode default model | `minimax/MiniMax-M3` (`defaults.yaml` `wizard.opencode_model`) until you choose another | docs/spec/08-resolutions.md R6; the owner's choice |
| opencode verification models | MiniMax `minimax/MiniMax-M3`, Anthropic `anthropic/claude-haiku-4-5`, OpenAI `openai/gpt-5-nano`, OpenRouter `openrouter/anthropic/claude-haiku-4.5`, DeepSeek `deepseek/deepseek-v4-flash`; a custom provider its first model | Cadence recommendation (cheap models of each catalogue) |

**Delivery** says whether the agent host has the value: `pending` until it writes it — **waiting for the agent
host** while no host is connected (none claimed work in the last 90 seconds) — then `written`; after Disconnect or
Remove, `removing` and `removed`; `failed` with the host's reason.

**Verification** is `none` after a new value, `pending` while the host runs the check, then `ok` or `failed` with the
agent's answer, the model and the time.

## Commands

| Command | API | What it does |
| --- | --- | --- |
| Save token, Add provider, Replace key | `agentCredentials.set` | Stores the value for the agent host to write; If-Match once the credential exists |
| Verify | `agentCredentials.verify` | The host runs a tiny real request through the agent, as a sandboxed session user behind the egress proxy |
| Default model for new projects | `agentCredentials.set` with `defaultModel` | opencode: one of the verified models |
| Disconnect, Remove | `agentCredentials.archive` | Inline confirm; the host deletes the files and the hosts leave the allowlist |

The same files can still be written from a terminal on the host, as a fallback:

```
docker compose run --rm -it agent-host login claude     # runs claude setup-token, asks for the token
docker compose run --rm -it agent-host login opencode   # runs opencode auth login
```

## Playbooks

### Connect Claude Code

1. On any machine with a browser and the Claude Code CLI, run `claude setup-token`, open the link it prints, sign in
   with the Claude account whose subscription the agents should use, and paste the code back. It prints a token that
   starts with `sk-ant-oat`.
2. In Settings → Agents → Claude Code, paste it into the token field and press **Save**. The field empties; the row
   shows the last four characters and delivery `pending`, then `written` once the agent host has it.
3. Press **Verify**. The host runs `claude -p "Reply OK"` on haiku with the token; `ok` shows the answer. New Claude
   Code sessions use the token from their next start.

### Add an opencode provider

1. Settings → Agents → opencode → **Add provider**, choose MiniMax (or Anthropic, OpenAI, OpenRouter, DeepSeek) and
   paste its API key (MiniMax: the Token Plan key from the MiniMax platform).
2. Press **Verify**. The host asks opencode for the provider's models (`opencode models <provider>`), then sends one
   tiny request (`opencode run`) with the provider's cheap model. The models appear under the provider.
3. Under **Default model for new projects**, keep `minimax/MiniMax-M3` or pick another verified model. The project
   wizard, the agent profile and new sessions start from it; existing projects keep their profile's model.

### A self-hosted vLLM (OpenAI-compatible)

1. Add provider → **OpenAI-compatible (custom base URL)**; give an id (lowercase, e.g. `vllm` — models become
   `vllm/<model>`), a display name and the base URL, e.g. `http://vllm.lan:8000/v1`. The key is optional.
2. Press **Verify**: the host reads `<base URL>/models` (the OpenAI-compatible list vLLM serves), writes them into the
   provider block, then lists and checks them through opencode as above. A server without `/models` fails Verify.

### The egress allowlist

Agents reach the internet only through the egress proxy (R4). Besides its static list (`CADENCE_EGRESS_ALLOW` in the
compose file), the proxy asks the control plane every 15 seconds which API hosts the configured providers need — for
example `api.minimax.io`, `openrouter.ai`, or your base URL's `vllm.lan:8000` — and lets those through too. Removing a
provider removes its hosts. An IP address in a base URL is allowed only exactly as written.

### Troubleshooting

- **Waiting for the agent host**: no agent host is connected. Check `docker compose ps agent-host` and its logs; the
  value is delivered as soon as it claims work again.
- **Verify failed: 401** (Claude "OAuth access token is invalid", a provider "authentication_error"): the token or key
  is wrong, revoked or expired. Create a new one, Save or Replace key, Verify again.
- **Expiry warning**: the Claude token is about a year old. Run `claude setup-token` again and save the new token
  before the old one stops working.
- **Verify failed: not on the allowlist / proxy refused**: the provider's host is not reachable through the proxy; wait
  15 seconds after adding it, and check the proxy's log line `egress refused`.
- **Delivery failed**: the host could not write the volume (disk, permissions); its reason is shown. Set the value
  again once fixed.

## Sources

- docs/spec/08-resolutions.md R3 (credential isolation), R4 (network sandbox), R6 (agent authentication), R9 (secret
  storage); docs/spec/05-agents.md "Drivers".
- Claude Code: `claude setup-token` (long-lived token for headless use) — [Claude Code docs](https://code.claude.com/docs).
- opencode providers and custom OpenAI-compatible providers — [opencode docs](https://opencode.ai/docs/providers/).
- vLLM's OpenAI-compatible server (`/v1/models`) — [vLLM docs](https://docs.vllm.ai/en/latest/serving/openai_compatible_server.html).
