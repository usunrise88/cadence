import { afterEach, describe, expect, it } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { useKeyedEstimate } from "./estimate";

afterEach(() => cleanup());

describe("useKeyedEstimate", () => {
  it("is fresh only for the values it answered and with no dry run pending", () => {
    const h = renderHook(({ key }: { key: string }) => useKeyedEstimate<number>(key), { initialProps: { key: "a" } });
    expect(h.result.current).toMatchObject({ estimate: undefined, fresh: false, stale: false, pending: false });
    let t = 0;
    act(() => void (t = h.result.current.begin()));
    expect(h.result.current.pending).toBe(true);
    act(() => h.result.current.settle(t, 1));
    expect(h.result.current).toMatchObject({ estimate: 1, fresh: true, stale: false });
    h.rerender({ key: "b" });
    expect(h.result.current).toMatchObject({ estimate: 1, fresh: false, stale: true });
    act(() => void (t = h.result.current.begin()));
    h.rerender({ key: "a" });
    // Back at the answered values, but a newer dry run (for b) is pending.
    expect(h.result.current.fresh).toBe(false);
    act(() => h.result.current.settle(t));
    // It failed: the answer for a stands.
    expect(h.result.current.fresh).toBe(true);
  });

  it("drops an answer to an older dry run", () => {
    const h = renderHook(({ key }: { key: string }) => useKeyedEstimate<number>(key), { initialProps: { key: "a" } });
    let older = 0;
    let newer = 0;
    act(() => void (older = h.result.current.begin()));
    h.rerender({ key: "b" });
    act(() => void (newer = h.result.current.begin()));
    act(() => h.result.current.settle(newer, 2));
    act(() => h.result.current.settle(older, 1));
    expect(h.result.current).toMatchObject({ estimate: 2, fresh: true });
  });
});
