import type { Approval } from "@/api/gen/types.gen";

// Fixture approvals for unit and component tests.
export function approval(over: Partial<Approval> = {}): Approval {
  return {
    id: "apr_0001",
    state: "pending",
    scope: "project",
    operation: "aliases.set",
    actor: { kind: "user", id: "usr_admin", name: "admin" },
    rule: "baseline-alias",
    reason: "changing a project's baseline changes what every gate compares against",
    request: { method: "PUT", path: "/api/projects/demo/aliases/baseline", headers: {}, body: { versionId: "ver_1" } },
    rev: 1,
    createdAt: "2026-09-30T10:00:00Z",
    expiresAt: "2026-10-01T10:00:00Z",
    ...over,
  };
}
