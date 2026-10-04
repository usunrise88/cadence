import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { guidelinesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import { GuidelinesPane } from "./Guidelines";

afterEach(() => cleanup());

describe("Guidelines in the Annotate view", () => {
  it("shows the pinned guidelines as Markdown when opened, raw HTML dropped", () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(guidelinesGetQueryKey({ path: { id: "anb_1" } }), {
      name: "default",
      path: "annotation/guidelines/default.md",
      commit: "0123456789abcdef",
      text: "# Calls\n\nWrite numbers **as words**.\n\n<img src=x onerror=alert(1)>\n",
      bytes: 60,
      truncated: false,
    });
    render(
      <QueryClientProvider client={qc}>
        <GuidelinesPane batchId="anb_1" />
      </QueryClientProvider>,
    );
    const toggle = screen.getByRole("button", { name: /Guidelines/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(document.querySelector('[data-slot="guidelines-text"]')).toBeNull();
    fireEvent.click(toggle);
    expect(toggle.textContent).toContain("annotation/guidelines/default.md at 01234567");
    const text = document.querySelector('[data-slot="guidelines-text"]');
    expect(text?.querySelector("h1")?.textContent).toBe("Calls");
    expect(text?.textContent).toContain("Write numbers as words.");
    expect(text?.querySelector("img")).toBeNull();
  });
});
