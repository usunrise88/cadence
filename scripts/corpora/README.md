# Corpora for the `corpora` mount

Host scripts that put source files on the staging host's corpora mount (`/cadence/corpora`, read-only to Cadence;
phase-4 plan, decision 1). Cadence indexes them in place through `sdp_ingest`; these scripts never touch Cadence.
Layout: `/cadence/corpora/<source>/<revision>/…` with a `SOURCE.yaml` (name, licence, kind, url, revision, languages).
Files must be world-readable: Cadence's workers read the mount as their own user (uid 65532), so a tar that extracts
with mode 640 makes ingest fail with "Permission denied" (`chmod -R a+rX /cadence/corpora`).

| Script | Source | Licence | Notes |
| --- | --- | --- | --- |
| `fleurs.sh <locale>` | google/fleurs, one locale, pinned revision | CC BY 4.0 | `<split>.tsv` + `<split>/*.wav`. The test split is the golden set's source: never ingest it for training (the freeze's leakage check refuses it anyway) |
| `calls_synth.py` | built: FLEURS dev speech (caller, channel 0) + the host's OmniVoice TTS (bot, channel 1) | CC BY 4.0 (derived) | 8 kHz G.711 μ-law stereo calls with `sdp_ingest` sidecars (`<id>.cadence.json`: roles, speakers, language, the bot's TTS script); `truth/` holds the caller references outside the ingested tree. Synthetic, never the telephone golden set |

```sh
scripts/corpora/fleurs.sh sr_rs
docker run --rm --network host --user $(id -u):$(id -g) -e HOME=/tmp -v $PWD:/src -v /cadence/corpora:/cadence/corpora \
  -w /src ghcr.io/astral-sh/uv:python3.12-bookworm-slim \
  uv run --with numpy --with scipy scripts/corpora/calls_synth.py \
  --fleurs /cadence/corpora/fleurs-sr/<rev> --out /cadence/corpora/calls-synth-sr --calls 40
```

On the stand (2026-10-03): `fleurs-sr/70bb2e84b976` (train 2.3 GB, dev, test) and `calls-synth-sr/2bb966076641`
(40 calls, about 50 minutes).
