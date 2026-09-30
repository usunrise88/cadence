#!/bin/sh
# cadence-agent-host host              run the agent host (default)
# cadence-agent-host login claude      put the Claude login into the agent-credentials volume (once)
# cadence-agent-host login opencode    put opencode's provider key (MiniMax) into the volume (once)
set -eu
cd /app
cmd="${1:-host}"
[ "$#" -gt 0 ] && shift
case "$cmd" in
  host) exec ./node_modules/.bin/tsx src/index.ts ;;
  login) exec ./node_modules/.bin/tsx src/login.ts "$@" ;;
  *) echo "usage: cadence-agent-host [host | login claude | login opencode]" >&2; exit 2 ;;
esac
