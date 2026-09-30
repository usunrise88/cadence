// The Cadence mark: a rounded accent square with a white wave below its centre — the same drawing as
// public/favicon.svg (keep the two in step). Colours come from the theme: the accent fill (indigo-9) and the
// on-solid white.

/** The wave's path in the 32 × 32 square, and its stroke. */
export const LOGO_WAVE = { d: "M8 20c3-8 5-8 8 0s5 8 8 0", width: 3 };

export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" aria-hidden className={className} data-slot="logo">
      <rect width="32" height="32" rx="6" style={{ fill: "var(--primary)" }} />
      <path d={LOGO_WAVE.d} strokeWidth={LOGO_WAVE.width} fill="none" strokeLinecap="round" style={{ stroke: "var(--cadence-on-solid)" }} />
    </svg>
  );
}
