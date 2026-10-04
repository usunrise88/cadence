# Host scripts

Scripts that run on the staging host, outside Cadence and outside GitHub Actions.

## nightly-gpu.sh

The NeMo pack's GPU tests (`pytest -m gpu`) and its conformance suite, run nightly from `main` in the nemo-speech
worker image, and the omni pack's GPU tests (`align_reference` with omniASR CTC 1B, `lid_classify@2` with VoxLingua107)
in the omni image built from the same commit, with one summary to Telegram. This replaces a self-hosted GitHub runner: the repository is public, so
a runner would let a fork's pull request run code on the host (owner decision 2026-10-03, `docs/spec/00-overview.md`
decision log).

- **What it uses.**
  - It clones `main` into `/cadence/nightly/repo`, never the working tree, and builds `cadence/worker:nightly` and
    `cadence/worker-omni:nightly` (about 11.6 GB; the build reuses its layers until the omni requirements change).
  - It reads the models from the stand's Hugging Face cache (volume `cadence-test_artifacts`, read-only). The NeMo
    tests run offline. The omni tests read the stand's cache first; a model it lacks is fetched once into
    `/cadence/nightly/hf` (VoxLingua107 is about 85 MB, until the stand's own `lid_classify` run caches it).
- **When it skips.**
  - The GPU tests need about 8 GB of free card memory, checked before each image's tests (the omni tests peak at
    about 2 GB). Too little is reported as a failure, so the summary never reads green without them.
  - The conformance needs the staging training cap (22 GB) plus margin. When a training job or another service
    holds the card, the conformance is skipped and the report says so.
- **Logs.** `/cadence/nightly/logs/<date>.log`, kept 30 days.
- **Locking.** A lock file stops two runs from overlapping.

Telegram: create `/cadence/nightly/telegram.env`, mode 600, holding the bot token and the chat id the Cadence bot
uses (Settings → Notifications). Without the file the summary goes only to the log. The command below creates it;
it reads the token without echoing it and keeps it out of the shell history:

```sh
mkdir -p /cadence/nightly && read -rs -p 'bot token: ' T && echo && read -r -p 'chat id: ' C && \
  printf 'TELEGRAM_BOT_TOKEN=%s\nTELEGRAM_CHAT_ID=%s\n' "$T" "$C" > /cadence/nightly/telegram.env && \
  chmod 600 /cadence/nightly/telegram.env && unset T C
```

Crontab entry (`crontab -e`). The host runs in UTC. 04:30 is after the 03:00 backups and the 01:30 agent evals:

```
30 4 * * * /home/era/cadence/cadence/scripts/nightly-gpu.sh >/dev/null 2>&1
```

Run it by hand with `scripts/nightly-gpu.sh`. To run the GPU tests only, set `NIGHTLY_SKIP_CONFORMANCE=1`; to leave
out the omni image and its tests, `NIGHTLY_SKIP_OMNI=1`.
