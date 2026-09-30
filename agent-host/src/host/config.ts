// Agent host configuration from the environment.
//
//   CADENCE_URL                 the control plane as the host and its agents reach it (http://control-plane:8080)
//   CADENCE_HOST_TOKEN_FILE     the agent host token (cah_…), written by the control plane at start (a shared volume)
//   CADENCE_HOST_TOKEN          the token itself, instead of the file (e.g. from `cadence admin host-token`)
//   CADENCE_HOST_DATA           session directories: worktrees and private HOMEs (/var/lib/cadence-host)
//   CADENCE_AGENT_CREDENTIALS   the agent-credentials volume (claude/, opencode/); unset = agents' default login (dev)
//   CADENCE_SESSION_UIDS        first:count of the per-session Unix users (20000:32); needs root; "off" disables
//   CADENCE_HOST_CAPACITY       live sessions at most (8)
//   CADENCE_HOST_ID             this process's id (default hostname-pid-start time)

import { readFileSync } from "node:fs";
import { hostname } from "node:os";
import { UidPool } from "./isolation.ts";

export interface HostConfig {
  baseUrl: string;
  token: string;
  dataDir: string;
  credentials?: string;
  uids?: UidPool;
  capacity: number;
  hostId: string;
}

export function loadConfig(env: NodeJS.ProcessEnv): HostConfig {
  const baseUrl = env.CADENCE_URL ?? env.CADENCE_API ?? "http://127.0.0.1:8080";
  let token = env.CADENCE_HOST_TOKEN?.trim() ?? "";
  if (!token && env.CADENCE_HOST_TOKEN_FILE) token = readFileSync(env.CADENCE_HOST_TOKEN_FILE, "utf8").trim();
  if (!token.startsWith("cah_")) {
    throw new Error("no agent host token: set CADENCE_HOST_TOKEN_FILE (the control plane writes it) or CADENCE_HOST_TOKEN");
  }
  const cfg: HostConfig = {
    baseUrl,
    token,
    dataDir: env.CADENCE_HOST_DATA ?? "/var/lib/cadence-host",
    capacity: Number(env.CADENCE_HOST_CAPACITY ?? "8"),
    hostId: env.CADENCE_HOST_ID ?? `${hostname()}-${process.pid}-${Date.now().toString(36)}`,
  };
  if (env.CADENCE_AGENT_CREDENTIALS) cfg.credentials = env.CADENCE_AGENT_CREDENTIALS;
  const uids = env.CADENCE_SESSION_UIDS ?? "20000:32";
  if (uids !== "off" && process.getuid?.() === 0) {
    const [first, count] = uids.split(":").map(Number);
    if (!first || !count) throw new Error(`CADENCE_SESSION_UIDS must be first:count, not ${uids}`);
    cfg.uids = new UidPool(first, count);
  }
  return cfg;
}
