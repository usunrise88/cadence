# S3 — Slate + Indigo theme for Dockview

Status: todo
Box: 1 day

## Goal
Map Radix Slate and Indigo tokens onto every Dockview surface, light and dark, including drop overlays and sashes.

## Setup
S1 app; @radix-ui/colors CSS; a custom Dockview theme object.

## Steps
1. Build theme.css mapping --dv-* variables to Slate steps per the Theming table. 2. Drag panels to see drop overlays; hover sashes; float a group. 3. Switch dark mode. 4. Run the contrast script on the pairs from the Theming table.

## Acceptance
No unthemed surface in either mode; contrast script passes 4.5:1 text and 3:1 non-text.

## Record
Full list of --dv-* variables the Dockview version exposes; pairs that failed and their fix.

## Result
_(fill in: what worked, numbers, surprises, what the spec should change)_
