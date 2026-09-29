// Topic patterns, same semantics as the control plane (internal/events): segments separated by dots; a trailing
// "*" matches one or more remaining segments; "*" alone matches every topic.

export function topicMatches(pattern: string, topic: string): boolean {
  if (pattern === "*") return true;
  const p = pattern.split(".");
  const t = topic.split(".");
  const last = p[p.length - 1];
  if (last === "*") {
    if (t.length < p.length) return false;
    return p.slice(0, -1).every((seg, i) => seg === t[i]);
  }
  return p.length === t.length && p.every((seg, i) => seg === t[i]);
}

/** The smallest set of patterns covering all given ones (drops patterns another pattern already covers). */
export function coverPatterns(patterns: Iterable<string>): string[] {
  const unique = [...new Set(patterns)].sort();
  if (unique.includes("*")) return ["*"];
  return unique.filter((p) => !unique.some((q) => q !== p && q.endsWith(".*") && covers(q, p)));
}

function covers(wide: string, narrow: string): boolean {
  const prefix = wide.slice(0, -1); // "run.123."
  return narrow.startsWith(prefix) && narrow.length > prefix.length;
}
