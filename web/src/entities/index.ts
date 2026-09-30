import { entities } from "@/shell/registries";
import { mixEntity } from "./mix";
import { projectEntity } from "./project";

// Entity manifests, one per kind that has operations. Kinds join as their phase implements them.
export function registerEntities(): void {
  entities.register(projectEntity);
  entities.register(mixEntity);
}
