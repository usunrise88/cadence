import { entities } from "@/shell/registries";
import { parseDocRef, type EntityData, type EntityManifest } from "@/shell/entity/manifest";

const none = (): { data?: EntityData; isLoading: boolean } => ({ isLoading: false });

/** Loads whatever entity a document reference points at, through its manifest (for tool panels). */
export function useEntity(doc: string | null): { data?: EntityData; manifest?: EntityManifest; isLoading: boolean } {
  const ref = doc ? parseDocRef(doc) : undefined;
  const manifest = ref ? entities.get(ref.kind) : undefined;
  // The hook identity changes only with the document kind; tool panels remount their body on kind change.
  const res = (manifest?.useData ?? none)(ref?.id ?? "");
  return { ...res, manifest };
}
