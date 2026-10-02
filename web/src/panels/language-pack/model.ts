import type { LanguagePack } from "@/api/gen/types.gen";

// Pure helpers of the Language pack document: the boost list editor's terms and the file the document opens on.

/** One term per line: trimmed, blank lines and `#` comments dropped, duplicates removed (first kept). */
export function parseTerms(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of text.split(/\r?\n/)) {
    const t = raw.trim();
    if (!t || t.startsWith("#") || seen.has(t)) continue;
    seen.add(t);
    out.push(t);
  }
  return out;
}

/** A boost list's domain is its file name, boost/<domain>.txt (BoostDomain in the contract). */
export function domainError(domain: string): string | undefined {
  if (!domain) return "Name the list";
  return /^[a-z0-9][a-z0-9-]{0,62}$/.test(domain) ? undefined : "Lower-case letters, digits and hyphens (boost/<domain>.txt)";
}

/** A weight within the safe range the editor offers (0 < w ≤ 10); empty keeps the list's own. */
export function weightError(weight: string): string | undefined {
  if (weight.trim() === "") return undefined;
  const w = Number(weight);
  return Number.isFinite(w) && w > 0 && w <= 10 ? undefined : "A number above 0, at most 10";
}

/** The file a pack opens on: normalizer.yaml when it exists, else the first file that is not a boost list. */
export function initialFile(p: Pick<LanguagePack, "files">): string | undefined {
  return (p.files.find((f) => f.path === "normalizer.yaml") ?? p.files.find((f) => !f.path.startsWith("boost/")) ?? p.files[0])?.path;
}
