import type { AnchorHTMLAttributes } from "react";
import { Streamdown } from "streamdown";
import { cn } from "@/lib/utils";

// The shell's renderer for Markdown that people or pipelines wrote and Cadence only shows (a dataset card, annotation
// guidelines): static, sanitised — raw HTML is dropped (skipHtml) and Streamdown's own URL check keeps javascript: and
// data: links out — and links open in a new tab without the opener.

function SafeLink({ href, children, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { node?: unknown }) {
  const { node: _node, ...props } = rest as typeof rest & { node?: unknown };
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" {...props}>
      {children}
    </a>
  );
}

const COMPONENTS = { a: SafeLink };

export type MarkdownProps = {
  children: string;
  className?: string;
  /** data-slot of the wrapper, for tests and tours. */
  slot?: string;
};

export function Markdown({ children, className, slot }: MarkdownProps) {
  return (
    <div className={cn("cadence-prose min-w-0 text-[13px]", className)} data-slot={slot}>
      <Streamdown mode="static" skipHtml controls={false} components={COMPONENTS}>
        {children}
      </Streamdown>
    </div>
  );
}
