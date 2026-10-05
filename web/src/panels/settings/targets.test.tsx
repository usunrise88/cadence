import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { deploymentTargetsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { DeploymentTargetList } from "@/api/gen/types.gen";
import { PanelContext } from "@/shell/panel/context";
import { DeploymentTargetsSection } from "./DeploymentTargetsSection";

const actor = { kind: "user" as const, id: "usr_admin" };
const list: DeploymentTargetList = {
  items: [
    {
      id: "dtg_s",
      name: "staging",
      kind: "staging",
      endpoint: "http://serving:8000",
      serves: [{ family: "fam", formats: ["fmt"], profiles: ["80ms"] }],
      server: { kind: "srv", version: "26.08" },
      slots: [],
      state: "active",
      health: { state: "down", since: "2026-11-01T00:00:00Z", detail: "connection refused" },
      servedModels: [{ model: "cadence-0123456789abcdef", deployableHash: "b3:" + "c".repeat(64), memoryMb: 9216, state: "in-use", leases: 2 }],
      rev: 1,
      createdBy: actor,
      createdAt: "2026-11-01T00:00:00Z",
      updatedAt: "2026-11-01T00:00:00Z",
    },
    {
      id: "dtg_p",
      name: "era-production",
      kind: "delivery",
      serves: [{ family: "fam", formats: ["fmt"], profiles: ["80ms"] }],
      server: { kind: "srv", version: "26.08" },
      repositoryPath: "/opt/era/models",
      slots: ["asr-he-il"],
      concurrency: 32,
      state: "active",
      chain: { records: 3, headSeq: 3, headHash: "f".repeat(64), pending: 1 },
      rev: 1,
      createdBy: actor,
      createdAt: "2026-11-01T00:00:00Z",
      updatedAt: "2026-11-01T00:00:00Z",
    },
  ],
  signingKeys: [{ id: "ed25519:" + "1".repeat(32), alg: "Ed25519", publicKeyPem: "-----BEGIN PUBLIC KEY-----\nMCow\n-----END PUBLIC KEY-----", state: "current", createdAt: "2026-11-01T00:00:00Z" }],
};

afterEach(cleanup);

describe("Settings → Deployment targets", () => {
  it("lists staging health and served models, delivery targets with their chain, and the public key", () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(deploymentTargetsListQueryKey({ query: { state: "all" } }), list);
    render(
      <QueryClientProvider client={qc}>
        <PanelContext.Provider value={{ instanceId: "settings", panelId: "settings", visible: false }}>
          <DeploymentTargetsSection />
        </PanelContext.Provider>
      </QueryClientProvider>,
    );
    const staging = screen.getByTestId("target-staging");
    expect(screen.getByTestId("target-health").textContent).toContain("down");
    expect(staging.textContent).toContain("connection refused");
    expect(staging.textContent).toContain("cadence-0123456789abcdef");
    expect(staging.textContent).toContain("9.0 GB");
    const prod = screen.getByTestId("target-era-production");
    expect(prod.textContent).toContain("asr-he-il");
    expect(prod.textContent).toContain("3 records, 1 pending");
    expect(screen.getByTestId("signing-key").textContent).toContain("BEGIN PUBLIC KEY");
    expect(screen.getByRole("button", { name: "Copy public key" })).toBeTruthy();
  });
});
