# Shell

Seven pieces around Dockview (see UI shell spec, "Shell concepts"):
panel registry · documents and tools · selection bus · workspaces · commands · agent bridge · chrome.

- `registry/` panel manifests; ESLint forbids panels importing each other or Dockview.
- `floating-snap/` the pure `computeSnap()` plus `dockview-adapter.ts`, the only file allowed to touch Dockview internals.
- `workspaces/` Dockview JSON + schema version + migrations; default layouts are code factories.
- `commands/` registry: id, title, icon, keys, enabled(ctx), run(ctx); every mutating command maps to one API operation.
- `agent/` Chat panel, selection → references bridge, attribution badges, draft Accept/Revert.
