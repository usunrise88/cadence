# Shell

Seven pieces around Dockview (docs/spec/10-ui-shell.md, "Shell concepts"): panel registry · documents and tools ·
selection bus · workspaces · commands · agent bridge · chrome.

| Directory | What lives there |
| --- | --- |
| `registry/` | `PanelManifest`, the panel registry and the alias map for renamed panels |
| `entity/` | `EntityManifest` (with `live` topics and cache patcher per kind), the three (+ container) state templates, the document primitives: `EntityHeader`, `ActionBar`, `StatusChip`, `NextStep`, `EntityTabs`, `EntityList`, `CompareView`, `EmptyState`, `PanelToolbar`, `ActorBadge`; drafts (`drafts.tsx`: `DraftOutline` with Accept / Revert and the diff on hover, `useDrafts`, `patchDrafts`, presence chip and notice) and edit requests (`edits.ts`) shared by every draftable kind |
| `panel/` | The panel SDK — the only shell surface panels import: `usePanel`, `useTopic`, selection hooks, `openDocument`, `runCommand`, `useEditRequest` |
| `selection/` | The selection bus (Zustand): active document, selection per document, pins |
| `dock/` | The Dockview host (public API only): `DockHost`, `PanelFrame`, `Tab`, `GroupActions`, window operations in `layout.ts` |
| `floating-snap/` | Pure `computeSnap()` and `dockview-adapter.ts`, the only file allowed to touch Dockview internals |
| `workspaces/` | Schema version + migrations, `normalizeLayout` (aliases, placeholders), default layouts as code factories, debounced If-Match persistence |
| `commands/` | The command registry (`<entity>.<verb>` = one API operation, `view.<name>` = client-only), key map with browser-reserved keys refused, built-in commands |
| `live/` | One multiplexed SSE stream, topic patterns, per-frame coalescing, cache patching |
| `chrome/` | Menu bar with project switcher and user menu, status bar with notification history and live region, palette, dialogs |
| `auth/` | `AuthGate` (first start, sign-in, back to sign-in on any 401), the session helpers that reset the cache and the event stream, the two-factor dialog |
| `theme/`, `help/`, `notifications/` | Theme mode (and popout injection), Help panel state, notices |

Agent bridge: `agents/attribution.ts` labels an agent actor ("opencode · session 9") and routes a badge's click to the tool call in Chat through a resolver the agent-session stream installs (`setAgentResolver`); drafts live in `entity/drafts.tsx`. Chat itself arrives with the agent-session stream.
