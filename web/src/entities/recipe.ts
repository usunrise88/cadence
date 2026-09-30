import { useQuery } from "@tanstack/react-query";
import { Page } from "iconoir-react";
import type { Recipe } from "@/api/gen/types.gen";
import { recipesGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";
import { useShell } from "@/shell/state";

// A recipe is a file of the project repository (docs/spec/02 "Domain model": "a versioned file in the recipes
// repository"). The document id is the file's path; the project is the one the workspace shows. Its "state" is the
// branch it is read at (main), so the header chip says where the content comes from.

export function recipeToEntity(r: Recipe, project: string): EntityData {
  const last = r.history[0];
  return {
    id: r.path,
    name: r.path,
    state: r.ref,
    updatedAt: last?.at,
    commit: r.commit,
    bytes: r.bytes,
    lastSubject: last?.message,
    lastAuthor: last?.author,
    project,
  };
}

const short = (v: unknown) => (typeof v === "string" && v ? v.slice(0, 7) : "—");

export const recipeEntity: EntityManifest = {
  kind: "recipe",
  apiEntity: "recipes",
  layer: "project",
  template: "container",
  verbs: [],
  facts: [
    { label: "Commit", value: (e) => short(e.commit) },
    { label: "Size", value: (e) => (typeof e.bytes === "number" ? `${e.bytes.toLocaleString()} B` : "—") },
    { label: "Last change", value: (e) => (typeof e.updatedAt === "string" ? new Date(e.updatedAt).toLocaleString() : "—") },
    { label: "By", value: (e) => (typeof e.lastAuthor === "string" ? e.lastAuthor : "—") },
  ],
  comparable: false,
  draftable: true,
  loopStep: () => "review",
  nextStep: () => ({ step: "review", title: "Review open session and sync branches below; accept a sync branch to bring templates up to date", command: "projects.sync" }),
  icon: Page,
  useData(id) {
    const project = useShell((s) => s.project) ?? "";
    const q = useQuery({ ...recipesGetOptions({ path: { p: project, path: id } }), enabled: !!id && !!project });
    return { data: q.data ? recipeToEntity(q.data, project) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
