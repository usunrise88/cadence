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
| `approvals/` | `ApprovalCard` (the Approvals panel lists it, Chat embeds it), the approvals cache patching, `usePendingApprovals` / `useDecidedApprovals`, the palette's focused approval |
| `theme/`, `help/`, `notifications/` | Theme mode (and popout injection), Help panel state, notices; `notifications/live.ts` turns approval, job and credential events into notification history and the polite live region |
| `charts/` | The chart primitive panels import (R53): `TimeSeriesChart` (uPlot) and `AnalyticsChart` (ECharts, lazy), with table view, CSV copy, keyboard cursor and text summary; the only place uPlot and ECharts are imported. API in `charts/README.md` |

Agent bridge (`agents/`): `sessions.ts` keeps sessions and transcripts in the query cache (`agentMessages.list` paged by `after`, patched from `agent.session.{id}` / `agent.sessions` one write per frame); `labels.ts` names known sessions ("claude-code · session 3") for badges and approval cards; `bridge.ts` owns which Chat shows which session (the workspace's `chat` pinned through the selection pins, others `chat:agent_session:<id>`), the composer drafts that Ctrl/Cmd+I and Ask agent fill, Explain this, opening references, and installs the attribution resolver (`setAgentResolver`: badge → the session's Chat at the tool call); `references.ts` is the `@<kind>:<id>[#part]` form and its links in Markdown; `live.ts` is the chrome's subscription that announces finished turns, pauses and ends in the polite live region. The agent commands are in `commands/agents.ts`; drafts live in `entity/drafts.tsx`.
