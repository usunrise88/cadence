export { Agent, pickOption, RestoreUnsupportedError } from "./agent.ts";
export type { AgentCapabilities, RestoreMode, SessionOptions, StartOptions, TurnResult } from "./agent.ts";
export { claudeDriver } from "./claude.ts";
export { makeOpencodeDriver, opencodeDriver } from "./opencode.ts";
export type * from "./types.ts";

import { claudeDriver } from "./claude.ts";
import { opencodeDriver } from "./opencode.ts";
import type { Driver, DriverName } from "./types.ts";

export const drivers: Record<DriverName, Driver> = { claude: claudeDriver, opencode: opencodeDriver };

// The driver of an agent profile's driver name (the contract's AgentDriver: claude-code | opencode).
export function driverFor(profileDriver: string): Driver {
  switch (profileDriver) {
    case "claude-code":
    case "claude":
      return claudeDriver;
    case "opencode":
      return opencodeDriver;
  }
  throw new Error(`no driver for ${profileDriver}`);
}
