import { commandHeaders, ProblemError } from "@/api/client";
import { viewsGet, viewsSet } from "@/api/gen/sdk.gen";
import type { SavedView } from "@/api/gen/types.gen";

// Saved searches (views.set): a named query per user per project. A save reads the current revision first so it
// updates an existing view (If-Match) or creates a new one (no If-Match), like saving a workspace.

export type SaveViewArgs = {
  name: string;
  query: string;
  description?: string;
};

export function isSaveViewArgs(v: unknown): v is SaveViewArgs {
  return !!v && typeof v === "object" && typeof (v as SaveViewArgs).name === "string" && typeof (v as SaveViewArgs).query === "string";
}

export async function saveView(project: string, args: SaveViewArgs): Promise<SavedView | undefined> {
  let rev: number | undefined;
  try {
    rev = (await viewsGet({ path: { p: project, name: args.name } })).data?.rev;
  } catch (err) {
    if (!(err instanceof ProblemError && err.status === 404)) throw err;
  }
  const res = await viewsSet({
    path: { p: project, name: args.name },
    body: {
      query: args.query,
      ...(args.description ? { description: args.description } : {}),
    },
    headers: commandHeaders(rev),
  });
  return res.data;
}
