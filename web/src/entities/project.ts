import { useQuery } from "@tanstack/react-query";
import { Folder } from "iconoir-react";
import type { Project } from "@/api/gen/types.gen";
import { projectsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";

export function projectToEntity(p: Project): EntityData {
  return {
    id: p.slug,
    name: p.name,
    state: p.archivedAt ? "archived" : "active",
    rev: p.rev,
    updatedAt: p.updatedAt,
    createdAt: p.createdAt,
    description: p.description,
    projectId: p.id,
  };
}

const date = (v: unknown) => (typeof v === "string" ? new Date(v).toLocaleDateString() : "—");

export const projectEntity: EntityManifest = {
  kind: "project",
  apiEntity: "projects",
  layer: "project",
  template: "container",
  verbs: [
    { verb: "edit", primary: true, enabled: (e) => (e.state === "archived" ? "Archived projects are read-only" : true) },
    { verb: "archive", enabled: (e) => (e.state === "archived" ? "Already archived" : true) },
  ],
  facts: [
    { label: "Slug", value: (e) => e.id },
    { label: "Revision", value: (e) => String(e.rev ?? "—") },
    { label: "Created", value: (e) => date(e.createdAt) },
    { label: "Updated", value: (e) => date(e.updatedAt) },
  ],
  comparable: false,
  draftable: false,
  loopStep: () => "prepare",
  nextStep: () => ({ step: "prepare", title: "Register a source or adopt a dataset version — the data block arrives with the agent loop (phase 1)" }),
  icon: Folder,
  useData(id) {
    const q = useQuery({ ...projectsGetOptions({ path: { p: id } }), enabled: !!id });
    return { data: q.data ? projectToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
