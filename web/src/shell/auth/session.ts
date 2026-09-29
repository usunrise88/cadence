import { hashKey, type QueryClient } from "@tanstack/react-query";
import { authGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { AuthStatus } from "@/api/gen/types.gen";
import { events } from "@/shell/registries";

// The browser session (docs/spec/06-platform.md "Authentication and access"). The session itself is an HttpOnly
// cookie the SPA never sees; what the SPA knows is auth.get's answer, cached under its query key. Signing in or out
// replaces that answer and drops every other cached query, so nothing of the previous session is shown.

export function authStatus(qc: QueryClient): AuthStatus | undefined {
  return qc.getQueryData<AuthStatus>(authGetQueryKey());
}

/** Drops every cached query except auth.get's, whose observers (the gate) must see the new status. */
function dropSessionData(qc: QueryClient): void {
  const auth = hashKey(authGetQueryKey());
  qc.removeQueries({ predicate: (q) => q.queryHash !== auth });
}

/** After a successful sign-in or first start: start from a clean cache with the new status. */
export function markSignedIn(qc: QueryClient, status: AuthStatus): void {
  dropSessionData(qc);
  qc.setQueryData(authGetQueryKey(), status);
}

/** After sign-out or a 401 (session expired or revoked): close the event stream, drop cached data, show sign-in. */
export function markSignedOut(qc: QueryClient): void {
  if (!authStatus(qc)?.actor) return; // already signed out: keep the sign-in form as it is
  events.close();
  dropSessionData(qc);
  qc.setQueryData<AuthStatus>(authGetQueryKey(), { setupRequired: false });
}
