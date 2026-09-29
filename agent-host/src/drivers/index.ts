export { Agent, pickOption, RestoreUnsupportedError } from "./agent.ts";
export type { AgentCapabilities, RestoreMode, SessionOptions, StartOptions, TurnResult } from "./agent.ts";
export { claudeDriver } from "./claude.ts";
export { makeOpencodeDriver, opencodeDriver } from "./opencode.ts";
export type * from "./types.ts";

import { claudeDriver } from "./claude.ts";
import { opencodeDriver } from "./opencode.ts";
import type { Driver, DriverName } from "./types.ts";

export const drivers: Record<DriverName, Driver> = { claude: claudeDriver, opencode: opencodeDriver };
