# Cadence spec — Coverage audit, risks, open questions, sources

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Coverage audit

Twelve blocks were missing at the audit of 2026-09-29; eleven are now specified in the sections above, and data governance under immutability is deliberately deferred to a later phase.

| Block | What is missing | Why it matters | Priority, phase |
| --- | --- | --- | --- |
| Authentication and access | How the admin logs in (passkey or password), session lifetime, how reviewer invitations work (link, expiry), how MCP session tokens and CLI keys are issued and revoked | Everything else assumes an identity; the reviewer role needs invitations | Must, phase 1 |
| Security and threat model | Agent sandbox specifics (container, filesystem limited to the worktree, egress allowlist), prompt injection through data an agent reads (transcripts, notes, help), secret handling end to end, TLS and reverse proxy, dependency pinning and SBOM | Agents read production transcripts and run shell; an injected instruction must not become a command | Must, phase 1 |
| Operations of Cadence itself | Install and upgrade, database and data migrations, backup and restore, disaster recovery, failure handling (worker crash, stuck queue, OOM retry with a smaller batch), release versioning against the NeMo container | A single-user tool still has to survive its own upgrades and a bad night | Must, phase 2 |
| Hot words and language packs | Boost lists (names, terms, brands) as a project asset applied at decode and evaluated; per-locale resources as registry assets: normalizer, inverse text normalisation, transliteration (Serbian Cyrillic and Latin), language-ID config, golden-set recipe | Hot words were a first-line requirement and appear only inside one playbook; every new locale needs the same resource set | Must, phase 2 |
| Task metrics and streaming metrics | Entity accuracy for names, numbers, dates and addresses on annotated golden sets; latency to final, partial-hypothesis stability, end-of-utterance quality; RTF and streams per card | A voice agent fails on a wrong phone number, not on average WER | Must, phase 3 |
| Annotation workflow | Building a golden set from own calls: sampling, guidelines, double annotation, adjudication, inter-annotator agreement; the reviewer role today only triages | Without it the telephone golden set cannot be produced | Should, phase 3 |
| Testing and quality strategy | Unit, contract and end-to-end layers; the smoke project as the e2e; fixtures for pipelines on tiny datasets; evals for skills and playbooks against both agents; Playwright for the shell | Spikes and contract tests exist; nothing says what CI runs every day | Should, phase 1 onward |
| Notifications | Channels beyond the in-app history (Telegram, email), routing by event class, quiet hours, digests; approvals while the admin is away | A nightly flywheel needs to reach a person | Should, phase 2 |
| Experiments and sweeps | Grouping runs into an experiment, parameter sweeps over a mix, comparison tables across many runs | Compare covers two entities; tuning needs ten | Should, phase 3 |
| Augmentation policy | Telephony band-limiting, codec simulation, noise and speed perturbation as step kinds with defaults and sources | The 8 kHz gap is measured; the fix is named only in a playbook | Should, phase 2 |
| Data governance under immutability | Deleting a caller's audio from immutable versions (tombstones, partial versions), access audit for who listened to what, data-subject requests | Immutability and deletion rights collide; the collision needs a rule | Could, phase 4 |
| Interoperability | Import of NeMo manifests, Lhotse cuts, Hugging Face datasets; export of datasets and models to the Hub or another project | Public data arrives in three formats; models may need to leave | Could, phase 4 |

Also unspecified but small: compute availability windows (pausing jobs in business hours on a shared card), the `cadence` CLI surface, capacity assumptions for the database and the search index, and decoder extensions such as end-of-utterance or speaker-gender tokens as a later training feature.

## Risks and open questions

The biggest unknowns are how complete the Claude Code ACP adapter is and how rough Nemotron 3.5 fine-tuning still is in NeMo; both get a spike before anything is built on them.

| Risk | Impact | Mitigation |
| --- | --- | --- |
| The Claude ACP adapter lags the native Claude Agent SDK | Missing hooks or permission detail for Claude sessions | Driver interface; native Claude driver for named gaps only |
| Agent runaway: loops, spend, GPU queue flooding | Cost, blocked card | Budgets, turn limits, inactivity timeout, one training slot per card |
| Nemotron 3.5 tooling moves fast (NeMo main vs release, prompt-key bugs) | Broken runs after upgrades | Pin the NeMo Speech container; smoke-test fine-tune and export on each upgrade |
| A TypeScript sidecar breaks the single-binary shape | More to deploy | A separate service in the same compose file, health-checked by the control plane; one version for all services |
| Licence mistakes in training data | Commercial use blocked later | Licence is a required source field; exports check every member's licence |
| Test leakage | Inflated scores, bad promotions | Runs cannot reference golden sets; fingerprint exclusion at freeze |

Spikes:

- [ ] A1: ACP client against `opencode acp` and `claude-agent-acp`: chat, tool-call diffs, permission round trip, cancel, resume
- [ ] A2: Cadence MCP server with ten generated tools; both agents create a mix and launch a dry-run
- [ ] A3: Nemotron 3.5 fine-tune on the staging card under a 24 GB cap, then ONNX export and a parity check
- [ ] A4: Outbox → SSE → cache patching with an agent editing a mix while the Mix panel is open

Open questions:

- [x] Add Chat, Agent sessions, Recipes, Sources, Golden sets and Approvals to the UI shell's panel catalogue
- [x] Stress marking and TTS datasets from the July concept: later phase, or dropped? — deferred to a later phase, out of v1
- [x] Default model for opencode sessions? — chosen per project in the wizard, editable in Agent settings
- [ ] Where production samples may be captured, and the retention limit — from call recordings on a mount, 90 days proposed, awaiting confirmation
- [x] Single user, or a team with roles? — single user in v1, roles deferred

## Sources

- [Nemotron 3.5 ASR model card](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b) — release date, languages, latency settings, licence
- [How to fine-tune Nemotron 3.5 ASR](https://huggingface.co/blog/nvidia/fine-tuning-nemotron-35-asr) — recipe, streaming evaluation, replay
- [NeMo Speech repository](https://github.com/NVIDIA-NeMo/Speech) — NeMo Speech 3.0 and the 26.07 container
- [NeMo ASR fine-tuning skill, PR #15733](https://github.com/NVIDIA-NeMo/NeMo/pull/15733)
- [Community Nemotron 3.5 fine-tune kit](https://github.com/Piyazon/nemotron-3.5-asr-streaming-0.6b-custom-finetune-kit) — learning-rate and tokenizer pitfalls
- [Kenyan-language adaptation of Nemotron 3.5](https://arxiv.org/abs/2607.18912) — leakage finding
- [Agent Client Protocol overview](https://agentclientprotocol.com/protocol/overview)
- [Claude Agent SDK TypeScript reference](https://code.claude.com/docs/en/agent-sdk/typescript)
- [opencode server](https://opencode.ai/docs/server/), [SDK](https://opencode.ai/docs/sdk/) and [skills](https://opencode.ai/docs/skills)
- [Lhotse dataloading in NeMo](https://docs.nvidia.com/nemo/speech/nightly/dataloaders.html)
- [NeMo Curator audio curation](https://docs.nvidia.com/nemo/curator/curate-audio)
- W&B Registry documentation — organisation-level registries, collections, aliases, lineage (pattern for the Cadence registry)
- M. Bisani, H. Ney, "Bootstrap estimates for confidence intervals in ASR performance evaluation", ICASSP 2004 — significance of WER deltas
- T. Gebru et al., "Datasheets for Datasets", CACM 2021; M. Mitchell et al., "Model Cards for Model Reporting", FAT* 2019 — generated cards
- ISO 9241-110:2020 interaction principles; WCAG 2.2 as ISO/IEC 40500:2025; RFC 9457 and RFC 9110 — standards cited in the UI shell tab
