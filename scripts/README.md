# Host scripts

Scripts that run on the staging host, outside Cadence and outside GitHub Actions.

## nightly-gpu.sh

The NeMo pack's GPU tests (`pytest -m gpu`) and its conformance suite, run nightly from `main` in the nemo-speech
worker image, with a summary to Telegram. This replaces a self-hosted GitHub runner: the repository is public, so
a runner would let a fork's pull request run code on the host (owner decision 2026-10-03, `docs/spec/00-overview.md`
decision log).

- **What it uses.**
  - It clones `main` into `/cadence/nightly/repo`, never the working tree, and builds `cadence/worker:nightly`.
  - It reads the model from the stand's Hugging Face cache (volume `cadence-test_artifacts`, read-only, offline).
- **When it skips.**
  - The GPU tests need about 8 GB of free card memory.
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

Run it by hand with `scripts/nightly-gpu.sh`. To run the GPU tests only, set `NIGHTLY_SKIP_CONFORMANCE=1`.
