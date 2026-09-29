---
paths:
  - "web/**"
---
# Web rules (Dockview shell)

Read docs/spec/10-ui-shell.md and docs/spec/11-ui-panels.md before adding or changing a panel.

- Stack: Vite, React, TanStack Router + Query, Zustand, Dockview (MIT), shadcn on Base UI, Radix Colors (Slate + Indigo), Iconoir, Streamdown, CodeMirror 6.
- A panel = a directory under `web/src/panels/<id>/` with a `manifest.ts` (PanelManifest) and its component. Panels never import each other and never import Dockview; only `web/src/shell/floating-snap/dockview-adapter.ts` may touch Dockview internals.
- Document panels use the shared primitives `EntityHeader`, `ActionBar`, `StatusChip`, `NextStep` driven by the entity manifest; lint rejects hand-drawn headers.
- Data: TanStack Query hooks from the generated client only; live updates patch the query cache from SSE events; subscribe only while the panel is visible.
- Commands: register in `web/src/shell/commands/`; a mutating command maps to exactly one generated API operation; keys never use browser-reserved shortcuts.
- Base UI: `render` prop, never `asChild`. Portals take the panel's `ownerDocument` (popout windows).
- Theming: tokens only (`theme.css`); no hex values in components; both modes must pass the contrast check.
- Accessibility: every drag has a keyboard and single-pointer alternative; hit areas ≥ 24×24 px; live region for agent status.
- Tests: Vitest for logic (computeSnap, registries, migrations), Playwright for shell behaviour; workspace fixtures round-trip through `fromJSON(toJSON())`.
