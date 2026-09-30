// Credential scan of a staged diff before each per-turn commit (R2): Cadence tokens by their fixed prefixes and the
// common provider key shapes. A hit refuses the commit; only the kind, file and line are reported, never the value.

export interface Finding {
  path: string;
  kind: string;
  line?: number;
}

// Cadence tokens are the prefix and 52 base32 characters (auth.NewToken); provider patterns follow their documented
// shapes loosely enough to catch real keys and strictly enough to skip prose.
const PATTERNS: ReadonlyArray<{ kind: string; re: RegExp }> = [
  { kind: "cst_", re: /\bcst_[a-z2-7]{20,}/ },
  { kind: "cdk_", re: /\bcdk_[a-z2-7]{20,}/ },
  { kind: "cwk_", re: /\bcwk_[a-z2-7]{20,}/ },
  { kind: "cah_", re: /\bcah_[a-z2-7]{20,}/ },
  { kind: "cws_", re: /\bcws_[a-z2-7]{20,}/ },
  { kind: "anthropic", re: /\bsk-ant-[A-Za-z0-9_-]{20,}/ },
  { kind: "openai", re: /\bsk-(?!ant-)(?:proj-)?[A-Za-z0-9_-]{32,}/ },
  { kind: "huggingface", re: /\bhf_[A-Za-z0-9]{30,}/ },
  { kind: "ngc", re: /\bnvapi-[A-Za-z0-9_-]{20,}/ },
  { kind: "github", re: /\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})/ },
  { kind: "aws", re: /\bAKIA[0-9A-Z]{16}\b/ },
  { kind: "private-key", re: /-----BEGIN (?:[A-Z]+ )?PRIVATE KEY-----/ },
];

// scanDiff reads `git diff --cached -U0` output and reports credentials on added lines.
export function scanDiff(diff: string): Finding[] {
  const out: Finding[] = [];
  let path = "";
  let line = 0;
  for (const raw of diff.split("\n")) {
    if (raw.startsWith("+++ ")) {
      path = raw.slice(4).replace(/^b\//, "");
      continue;
    }
    const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(raw);
    if (hunk?.[1]) {
      line = Number(hunk[1]);
      continue;
    }
    if (raw.startsWith("+")) {
      const text = raw.slice(1);
      for (const p of PATTERNS) {
        if (p.re.test(text)) out.push({ path, kind: p.kind, line });
      }
      line++;
    }
  }
  return out;
}
