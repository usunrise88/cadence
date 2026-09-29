import { useSnap } from "./dockview-adapter";

// Guide lines for active snaps: 1 px, accent step 9, at most one per axis, above the floating windows and never
// intercepting the pointer (docs/spec/10-ui-shell.md "Snapping for floating windows").
export function Guides() {
  const guides = useSnap((s) => s.guides);
  if (guides.length === 0) return null;
  return (
    <div aria-hidden className="cadence-guides pointer-events-none absolute inset-0">
      {guides.map((g) => (
        <div
          key={g.axis}
          data-guide={g.axis}
          className="absolute bg-[var(--cadence-guide)]"
          style={
            g.axis === "x"
              ? { left: g.pos, top: g.from, width: 1, height: g.to - g.from }
              : { top: g.pos, left: g.from, height: 1, width: g.to - g.from }
          }
        />
      ))}
    </div>
  );
}
