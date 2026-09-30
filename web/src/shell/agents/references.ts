import type { AgentReference } from "@/api/gen/types.gen";
import { parseDocRef } from "@/shell/entity/manifest";
import type { Selection } from "@/shell/selection/store";

// References between the UI and the agents (docs/spec/01-principles.md rule 7, docs/spec/05-agents.md "Context
// bridge"): `@<kind>:<id>[#<part>]`, the textual form the control plane parses (internal/sessions/refs.go). A
// selection attaches as references; references in the agent's replies render as links that open the document.

/** Short kinds people and agents write → the entity kind documents and search use (same map as the server). */
const KIND_ALIASES: Record<string, string> = {
  utt: "utterance",
  apr: "approval",
  ses: "agent_session",
  session: "agent_session",
  ver: "version",
  dataset: "version",
  help: "help_article",
};

/** The short kind a reference writes for an entity kind (`help_article` → `help`, `agent_session` → `session`). */
const SHORT_KINDS: Record<string, string> = { help_article: "help", agent_session: "session", utterance: "utt" };

const REF = /^@([a-z][a-z_]*):([A-Za-z0-9][A-Za-z0-9._/-]*)(?:#(.+))?$/;

export type ParsedReference = { kind: string; id: string; fragment?: string };

/** Parses `@mix:mix_1#groups/0` into kind (resolved through the aliases), id and the part after `#`. */
export function parseReference(ref: string): ParsedReference | undefined {
  const m = REF.exec(ref.trim());
  if (!m) return undefined;
  const kind = KIND_ALIASES[m[1]!] ?? m[1]!;
  return { kind, id: m[2]!, ...(m[3] ? { fragment: m[3] } : {}) };
}

export function formatReference(kind: string, id: string, fragment?: string): string {
  const k = SHORT_KINDS[kind] ?? kind;
  return `@${k}:${id}${fragment ? `#${fragment}` : ""}`;
}

/** The document reference (`kind:id`) a reference opens. */
export function referenceDoc(ref: string): string | undefined {
  const p = parseReference(ref);
  return p ? `${p.kind}:${p.id}` : undefined;
}

/** A reference for an open document (`mix:mix_1`) and, optionally, the item selected inside it. */
export function referenceFor(doc: string, item?: string, label?: string): AgentReference | undefined {
  const d = parseDocRef(doc);
  if (!d || !REF.test(formatReference(d.kind, d.id))) return undefined;
  const ref = formatReference(d.kind, d.id, item);
  if (!REF.test(ref)) return undefined;
  return { ref, ...(label ? { label } : {}) };
}

/** What a selection attaches: the document, plus the item inside it as the reference's part. */
export function selectionReferences(sel: Selection | null, extra: { kind: string; id: string; label?: string }[] = []): AgentReference[] {
  const out: AgentReference[] = [];
  if (sel) {
    const r = referenceFor(sel.doc, sel.item);
    if (r) out.push(r);
  }
  for (const e of extra) {
    const r = referenceFor(`${e.kind}:${e.id}`, undefined, e.label);
    if (r) out.push(r);
  }
  return dedupeReferences(out);
}

export function dedupeReferences(refs: AgentReference[]): AgentReference[] {
  const seen = new Set<string>();
  return refs.filter((r) => (seen.has(r.ref) ? false : (seen.add(r.ref), true)));
}

/** The chip text: the label when the UI knows one, else the reference shortened in the middle. */
export function referenceChipLabel(r: AgentReference): string {
  if (r.label) return r.label;
  return r.ref.length > 28 ? `${r.ref.slice(0, 18)}…${r.ref.slice(-8)}` : r.ref;
}

// A reference inside prose: preceded by the start, whitespace or an opening bracket; the id may not end in
// sentence punctuation ("see @recipe:project.yaml." links project.yaml).
const INLINE = /(^|[\s([{"'>])(@[a-z][a-z_]*:[A-Za-z0-9](?:[A-Za-z0-9._/-]*[A-Za-z0-9_/-])?(?:#[A-Za-z0-9._/\-[\],:=]*[A-Za-z0-9_\]/-])?)/g;

/** The href a linked reference carries in rendered Markdown (a fragment, so no sanitiser strips it). */
export const REF_HREF_PREFIX = "#cadence-ref=";

/**
 * Turns references in an agent's Markdown into links (`[@mix:mix_1](#cadence-ref=@mix:mix_1)`), leaving code spans,
 * fenced code and existing links alone.
 */
export function linkifyReferences(markdown: string): string {
  const out: string[] = [];
  // Split off fenced blocks (``` or ~~~, closed or still streaming) and inline code spans; transform the rest.
  const parts = markdown.split(/(```[\s\S]*?(?:```|$)|~~~[\s\S]*?(?:~~~|$)|`[^`\n]*`)/);
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i]!;
    if (i % 2 === 1) {
      out.push(part);
      continue;
    }
    out.push(
      part.replace(INLINE, (whole, lead: string, ref: string, offset: number, s: string) => {
        // Already inside a Markdown link's text or target: [..@x..](..) — leave it.
        const before = s.slice(0, offset + lead.length);
        if (/\[[^\]]*$/.test(before) || /\]\([^)]*$/.test(before)) return whole;
        if (!parseReference(ref)) return whole;
        return `${lead}[${ref}](${REF_HREF_PREFIX}${encodeURIComponent(ref)})`;
      }),
    );
  }
  return out.join("");
}

/** The reference a rendered link points at, if it is one of ours. */
export function referenceFromHref(href: string | undefined): string | undefined {
  if (!href) return undefined;
  const i = href.indexOf(REF_HREF_PREFIX);
  if (i < 0) return undefined;
  try {
    return decodeURIComponent(href.slice(i + REF_HREF_PREFIX.length));
  } catch {
    return undefined;
  }
}
