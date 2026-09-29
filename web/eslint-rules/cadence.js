// Local ESLint rules that enforce the shell's architecture (docs/spec/10-ui-shell.md, .claude/rules/web.md).
import path from "node:path";

const SRC = path.resolve(import.meta.dirname, "../src");

function panelOf(file) {
  const rel = path.relative(path.join(SRC, "panels"), file);
  if (rel.startsWith("..") || path.isAbsolute(rel)) return undefined;
  const [dir] = rel.split(path.sep);
  return dir && !dir.includes(".") ? dir : undefined;
}

function resolveImport(file, source) {
  if (source.startsWith("@/")) return path.join(SRC, source.slice(2));
  if (source.startsWith(".")) return path.resolve(path.dirname(file), source);
  return undefined;
}

/** A panel may not import another panel (panels know only the shell). */
const noCrossPanelImports = {
  meta: { type: "problem", messages: { cross: "Panel “{{from}}” imports panel “{{to}}”; panels never import each other — go through the shell (selection bus, commands)." } },
  create(context) {
    const file = context.filename;
    const from = panelOf(file);
    if (!from) return {};
    const check = (node, source) => {
      const target = resolveImport(file, source);
      const to = target ? panelOf(target) : undefined;
      if (to && to !== from) context.report({ node, messageId: "cross", data: { from, to } });
    };
    return {
      ImportDeclaration: (n) => check(n, n.source.value),
      ImportExpression: (n) => n.source.type === "Literal" && check(n, n.source.value),
      ExportNamedDeclaration: (n) => n.source && check(n, n.source.value),
      ExportAllDeclaration: (n) => check(n, n.source.value),
    };
  },
};

export default { rules: { "no-cross-panel-imports": noCrossPanelImports } };
