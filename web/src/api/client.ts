import { client } from "./gen/client.gen";
import type { Problem } from "./gen/types.gen";

// The one configured API client. Panels never call it directly: they use the generated TanStack Query options
// (`@/api/gen/@tanstack/react-query.gen`) and commands (`@/shell/commands`).

export type { Problem };

const PROBLEM_BASE = "https://cadence.local/help/errors/";

/** An API error in RFC 9457 form. `helpId` is the help article for its type (errors.<slug>). */
export class ProblemError extends Error {
  readonly problem: Problem;
  constructor(problem: Problem) {
    super(problem.detail ?? problem.title);
    this.name = "ProblemError";
    this.problem = problem;
  }
  get status(): number {
    return this.problem.status;
  }
  get slug(): string {
    return this.problem.type.startsWith(PROBLEM_BASE) ? this.problem.type.slice(PROBLEM_BASE.length) : "internal";
  }
  get helpId(): string {
    return `errors.${this.slug}`;
  }
}

function isProblem(x: unknown): x is Problem {
  return !!x && typeof x === "object" && typeof (x as Problem).type === "string" && typeof (x as Problem).status === "number";
}

function hex(bytes: number): string {
  const a = new Uint8Array(bytes);
  crypto.getRandomValues(a);
  return [...a].map((b) => b.toString(16).padStart(2, "0")).join("");
}

/** W3C trace context for one UI action; the control plane continues the trace (docs/spec/06-platform.md). */
export function newTraceparent(): string {
  return `00-${hex(16)}-${hex(8)}-01`;
}

export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

/** The CSRF header: every browser request carries it; the server requires it on cookie-authenticated mutations. */
export const CLIENT_HEADER = "Cadence-Client";

const unauthenticatedListeners = new Set<() => void>();

/**
 * Called when a request outside sign-in answers 401: the session expired, was signed out elsewhere or revoked.
 * The app shows the sign-in screen.
 */
export function onUnauthenticated(fn: () => void): () => void {
  unauthenticatedListeners.add(fn);
  return () => unauthenticatedListeners.delete(fn);
}

/** Sign-in operations answer 401 for a wrong password; that is the form's business, not a lost session. */
function isAuthRequest(request: Request | undefined): boolean {
  if (!request) return false;
  const path = new URL(request.url, location.origin).pathname;
  return /\/auth(?:[:/]|$)/.test(path);
}

let configured = false;

export function configureApiClient(baseUrl = "/api"): void {
  if (configured) return;
  configured = true;
  // Same-origin cookies carry the session; the SPA never sees the token (HttpOnly).
  client.setConfig({ baseUrl, throwOnError: true, credentials: "same-origin" });
  client.interceptors.request.use((request) => {
    if (!request.headers.has("traceparent")) request.headers.set("traceparent", newTraceparent());
    request.headers.set(CLIENT_HEADER, "web");
    return request;
  });
  client.interceptors.error.use((error, response, request) => {
    if (response?.status === 401 && !isAuthRequest(request)) for (const fn of unauthenticatedListeners) fn();
    if (isProblem(error)) return new ProblemError(error);
    const status = response?.status ?? 0;
    return new ProblemError({
      type: `${PROBLEM_BASE}${status === 0 ? "unreachable" : "internal"}`,
      title: status === 0 ? "Control plane unreachable" : `HTTP ${status}`,
      status,
      detail: typeof error === "string" ? error : undefined,
    });
  });
}

/** Headers for one command: a fresh idempotency key and a trace, plus If-Match when the change is based on a rev. */
export function commandHeaders(rev?: number): { "Idempotency-Key": string; "If-Match": string; traceparent: string } & Record<string, string> {
  const h: Record<string, string> = { "Idempotency-Key": newIdempotencyKey(), traceparent: newTraceparent() };
  if (rev !== undefined) h["If-Match"] = `"${rev}"`;
  return h as { "Idempotency-Key": string; "If-Match": string; traceparent: string };
}

/** Headers for a command based on an opaque version (a branch head sha): If-Match carries it quoted. */
export function commandHeadersAt(etag: string): { "Idempotency-Key": string; "If-Match": string; traceparent: string } {
  return { "Idempotency-Key": newIdempotencyKey(), traceparent: newTraceparent(), "If-Match": `"${etag}"` };
}

/** Parses an ETag ("3", W/"3") into a revision number. */
export function revFromEtag(etag: string | null | undefined): number | undefined {
  if (!etag) return undefined;
  const m = /^(?:W\/)?"?(\d+)"?$/.exec(etag.trim());
  return m ? Number(m[1]) : undefined;
}
