import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";
import { act, cleanup, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { runsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import { useSelection } from "@/shell/selection/store";
import { useActiveRun } from "./useActiveRun";

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(runsListQueryKey({ path: { p: "alpha" }, query: { limit: 1 } }), { items: [{ id: "run_alpha_new" }] });
  qc.setQueryData(runsListQueryKey({ path: { p: "beta" }, query: { limit: 1 } }), { items: [{ id: "run_beta_new" }] });
  useSelection.setState({ activeDoc: null, selections: {}, pins: {} });
});
afterEach(() => cleanup());

function hook(project: string) {
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return renderHook(({ p }: { p: string }) => useActiveRun("metrics", p), { wrapper, initialProps: { p: project } });
}

describe("useActiveRun", () => {
  it("follows a Run document, keeps it after the focus moves on, and starts from the newest run", () => {
    const h = hook("alpha");
    expect(h.result.current.runId).toBe("run_alpha_new");
    act(() => useSelection.setState({ activeDoc: "run:run_a1" }));
    expect(h.result.current.runId).toBe("run_a1");
    act(() => useSelection.setState({ activeDoc: "mix:mix_1" }));
    expect(h.result.current.runId).toBe("run_a1");
  });

  it("forgets the last run when the project changes", () => {
    const h = hook("alpha");
    act(() => useSelection.setState({ activeDoc: "run:run_a1" }));
    act(() => useSelection.setState({ activeDoc: null }));
    expect(h.result.current.runId).toBe("run_a1");
    h.rerender({ p: "beta" });
    expect(h.result.current.runId).toBe("run_beta_new");
    // Back in the first project nothing of the second leaks in either.
    act(() => useSelection.setState({ activeDoc: "run:run_b1" }));
    act(() => useSelection.setState({ activeDoc: null }));
    expect(h.result.current.runId).toBe("run_b1");
    h.rerender({ p: "alpha" });
    expect(h.result.current.runId).toBe("run_alpha_new");
  });
});
