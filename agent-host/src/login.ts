// Puts an agent's own login into the agent-credentials volume, once (R3, R6). The host copies it into each
// session's private HOME; nothing here reaches Cadence's secrets or the user's own ~/.claude.
//
//   docker compose run --rm -it agent-host login claude     the owner's Claude subscription (`claude setup-token`)
//   docker compose run --rm -it agent-host login opencode   `opencode auth login`: MiniMax and the Token Plan key
//
// Settings → Agents (the admin's UI) is the usual way now: the host writes the same files from its credential tasks
// (src/host/credentials.ts); these commands stay as the fallback.
//
// CADENCE_AGENT_CREDENTIALS names the volume's mount (/agent-credentials in the image). Each driver knows how its
// agent logs in (Driver.login).

import { drivers } from "./drivers/index.ts";
import type { DriverName } from "./drivers/types.ts";

export async function login(argv: readonly string[]): Promise<void> {
  const root = process.env.CADENCE_AGENT_CREDENTIALS;
  if (!root) throw new Error("set CADENCE_AGENT_CREDENTIALS to the agent-credentials volume");
  const name = argv[0] as DriverName | undefined;
  const driver = name ? drivers[name] : undefined;
  if (!driver?.login) throw new Error(`usage: login ${Object.keys(drivers).join(" | ")}`);
  await driver.login(root);
}

if (import.meta.url === `file://${process.argv[1]}`) {
  login(process.argv.slice(2)).catch((err: unknown) => {
    process.stderr.write(`login: ${err instanceof Error ? err.message : String(err)}\n`);
    process.exit(1);
  });
}
