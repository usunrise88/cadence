import { CommandRegistry } from "@/shell/commands/registry";
import { createEntityRegistry } from "@/shell/entity/manifest";
import { EventStream } from "@/shell/live/events";
import { createPanelRegistry } from "@/shell/registry/panels";

// The shell's singletons. Panels register themselves through their manifests (src/panels/index.ts); everything
// else reads from here.
export const panels = createPanelRegistry();
export const entities = createEntityRegistry();
export const commands = new CommandRegistry();
export const events = new EventStream();
