import { entities } from "@/shell/registries";
import { experimentEntity } from "./experiment";
import { evalEntity } from "./eval";
import { mixEntity } from "./mix";
import { projectEntity } from "./project";
import { recipeEntity } from "./recipe";
import { goldenSetEntity, languagePackEntity, modelEntity } from "./registry-eval";
import { runEntity } from "./run";

// Entity manifests, one per kind that has operations. Kinds join as their phase implements them.
export function registerEntities(): void {
  entities.register(projectEntity);
  entities.register(mixEntity);
  entities.register(recipeEntity);
  entities.register(runEntity);
  entities.register(experimentEntity);
  // Phase 3: evaluation.
  entities.register(evalEntity);
  entities.register(goldenSetEntity);
  entities.register(modelEntity);
  entities.register(languagePackEntity);
}
