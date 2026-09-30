import { useQuery } from "@tanstack/react-query";
import { Folder } from "iconoir-react";
import type { Project } from "@/api/gen/types.gen";
import { projectsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";

export function projectToEntity(p: Project): EntityData {
  return {
    id: p.slug,
    name: p.name,
    state: p.archivedAt ? "archived" : (p.state ?? "active"),
    rev: p.rev,
    updatedAt: p.updatedAt,
    createdAt: p.createdAt,
    description: p.description,
    projectId: p.id,
    locales: p.locales?.join(", "),
    domain: p.domain,
    baseModel: p.baseModel ? `${p.baseModel.hfRepo}@${p.baseModel.revision.slice(0, 7)}` : undefined,
    repository: p.repository?.kind,
    bootstrapError: p.bootstrapError,
  };
}

const date = (v: unknown) => (typeof v === "string" ? new Date(v).toLocaleDateString() : "—");
const text = (v: unknown) => (typeof v === "string" && v ? v : "—");

const readOnly = (e: EntityData): true | string => {
  if (e.state === "archived") return "Archived projects are read-only";
  if (e.state === "bootstrapping") return "The repository is still being bootstrapped";
  if (e.state === "failed") return "The bootstrap failed; the project has no repository";
  return true;
};

export const projectEntity: EntityManifest = {
  kind: "project",
  apiEntity: "projects",
  layer: "project",
  template: "container",
  verbs: [
    { verb: "edit", primary: true, enabled: (e) => (e.state === "archived" ? "Archived projects are read-only" : true) },
    { verb: "note", enabled: readOnly },
    { verb: "sync", enabled: readOnly },
    { verb: "archive", enabled: (e) => (e.state === "archived" ? "Already archived" : true) },
  ],
  facts: [
    { label: "Locales", value: (e) => text(e.locales) },
    { label: "Base model", value: (e) => text(e.baseModel) },
    { label: "Repository", value: (e) => (e.repository ? `${String(e.repository)} · main` : "—") },
    { label: "Updated", value: (e) => date(e.updatedAt) },
  ],
  comparable: false,
  draftable: false,
  loopStep: () => "prepare",
  nextStep: (e) => {
    if (e.state === "bootstrapping") return { step: "prepare", title: "The bootstrap job is writing the repository; this document updates when it is done" };
    if (e.state === "failed") return { step: "prepare", title: `The bootstrap failed: ${text(e.bootstrapError)}` };
    return { step: "prepare", title: "Review the agent profile and AGENTS.md, then register a source or adopt a dataset version", command: "agentProfile.edit" };
  },
  icon: Folder,
  useData(id) {
    const q = useQuery({ ...projectsGetOptions({ path: { p: id } }), enabled: !!id });
    return { data: q.data ? projectToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
