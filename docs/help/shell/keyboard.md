---
title: Keyboard
summary: The default key map; every key is also a palette command, and browser-reserved keys are never used.
contexts: [shell:keyboard]
---

## What this is

Every command can be run from the keyboard. Keys are defined once in the command registry; the shortcut sheet
(Ctrl/Cmd+/) is generated from it.

## Place in the loop

Any step.

## Fields and defaults

| Keys | Action |
| --- | --- |
| Ctrl/Cmd+K | Palette: text searches, `>` commands, `?` help, `@` projects |
| Ctrl/Cmd+Alt+P | Switch project |
| F6 / Shift+F6 | Next / previous group, floating windows included |
| Ctrl/Cmd+Alt+] / [ | Next / previous tab in the group |
| Alt+W | Close panel |
| Ctrl/Cmd+\ | Hide or show all tool panels |
| Ctrl/Cmd+Shift+1…5 | Training, Eval, Data, Triage, Ops workspace |
| Ctrl/Cmd+Shift+; | Toggle snapping |
| ? | Help for the focused panel |
| Ctrl/Cmd+/ | Keyboard shortcuts |
| Ctrl+M | Move the active panel with the keyboard (arrows pick the target, Enter docks, Esc cancels) |
| Ctrl/Cmd+I | Focus Chat and attach the current selection as references |
| Ctrl/Cmd+. | Stop the agent's current turn (the session the current Chat shows) |
| Enter / Backspace | On a focused approval card: approve (allow) once / deny |

## Commands

Ctrl/Cmd+W, T, N, Ctrl+Tab and Ctrl/Cmd+Shift+P belong to the browser and are never assigned.

## Playbooks

None.

## Sources

- WCAG 2.2 SC 2.1.1 Keyboard.
- Cadence recommendation — docs/spec/10-ui-shell.md "Accessibility and input"; R37 in docs/spec/08-resolutions.md.
