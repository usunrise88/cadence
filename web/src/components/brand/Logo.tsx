// The Cadence mark: a rounded accent square with a white wave below and right of its centre — the same drawing as
// public/favicon.svg (keep the two in step). Colours come from the theme: the accent fill (indigo-9) and the
// on-solid white.

/** The wave's path in the 32 × 32 square, and its stroke. */
export const LOGO_WAVE = { d: "M13 21c2.6-6.8 4.4-6.8 7 0s4.4 6.8 7 0", width: 2.75 };

export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" aria-hidden className={className} data-slot="logo">
      <rect width="32" height="32" rx="6" style={{ fill: "var(--primary)" }} />
      <path d={LOGO_WAVE.d} strokeWidth={LOGO_WAVE.width} fill="none" strokeLinecap="round" style={{ stroke: "var(--cadence-on-solid)" }} />
    </svg>
  );
}
