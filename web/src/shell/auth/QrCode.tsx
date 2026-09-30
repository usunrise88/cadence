import { useMemo } from "react";
import { encode } from "uqr";

// A QR code drawn in the browser from text (an otpauth URI): nothing leaves the page. Dark modules on a light field in
// both themes — authenticator apps read inverted codes poorly.

export function QrCode({ text, label, size = 200 }: { text: string; label: string; size?: number }) {
  const { path, n } = useMemo(() => {
    const qr = encode(text, { ecc: "M", border: 2 });
    let d = "";
    qr.data.forEach((row, y) =>
      row.forEach((dark, x) => {
        if (dark) d += `M${x} ${y}h1v1h-1z`;
      }),
    );
    return { path: d, n: qr.size };
  }, [text]);
  return (
    <svg role="img" aria-label={label} viewBox={`0 0 ${n} ${n}`} width={size} height={size} shapeRendering="crispEdges" className="rounded-md" data-slot="qr-code">
      <rect width={n} height={n} style={{ fill: "var(--cadence-qr-light)" }} />
      <path d={path} style={{ fill: "var(--cadence-qr-dark)" }} />
    </svg>
  );
}
