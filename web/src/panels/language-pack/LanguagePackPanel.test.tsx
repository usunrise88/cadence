import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { LanguagePack } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { LanguagePackPanel } from "./LanguagePackPanel";
import { domainError, initialFile, parseTerms, weightError } from "./model";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo" }));

const PACK: LanguagePack = {
  locale: "he-IL",
  path: "lang/he-IL",
  sha: "0123456789abcdef",
  commit: "fedcba9876543210",
  files: [
    { path: "README.md", bytes: 10, content: "# he-IL" },
    { path: "normalizer.yaml", bytes: 30, content: "scoring:\n  normalizer: normalizer/he-IL\n" },
    { path: "boost/names.txt", bytes: 20, content: "# weight: 2\nדוד\nשרה\n" },
  ],
  boost: [{ domain: "names", path: "boost/names.txt", weight: 2, terms: ["דוד", "שרה"], sha256: "aa".repeat(32) }],
  scoring: { normalizer: "normalizer/he-IL", versionId: "ver_n", version: "2026-10-02.abc" },
  issues: [],
};

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  runCommand.mockReset();
});
afterEach(() => cleanup());

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "language-pack:language_pack:he-IL", panelId: "language-pack", doc: "language_pack:he-IL", visible: false }}>
          <LanguagePackPanel panelId="language-pack" instanceId="language-pack:language_pack:he-IL" doc="language_pack:he-IL" entity={{ id: "he-IL", name: "he-IL", state: "active", project: "demo", pack: PACK }} />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Language pack model", () => {
  it("parses one term per line without blanks, comments or duplicates", () => {
    expect(parseTerms(" דוד \n\n# weight: 2\nשרה\nדוד\r\nתל אביב")).toEqual(["דוד", "שרה", "תל אביב"]);
  });
  it("validates list names and weights", () => {
    expect(domainError("names")).toBeUndefined();
    expect(domainError("Names")).toMatch(/Lower-case/);
    expect(domainError("")).toBe("Name the list");
    expect(weightError("")).toBeUndefined();
    expect(weightError("2.5")).toBeUndefined();
    expect(weightError("0")).toBeDefined();
    expect(weightError("abc")).toBeDefined();
  });
  it("opens on normalizer.yaml", () => {
    expect(initialFile(PACK)).toBe("normalizer.yaml");
    expect(initialFile({ files: [{ path: "boost/a.txt", bytes: 1, content: "" }] })).toBe("boost/a.txt");
  });
});

describe("Language pack document", () => {
  it("edits a file: Check (dry run) first, then Commit with the pack's commit as If-Match", async () => {
    runCommand.mockResolvedValue({ ...PACK, sha: "1111111" });
    wrap();
    expect(document.querySelector('[data-slot="pack-file"]')!.textContent).toContain("normalizer/he-IL");
    fireEvent.click(screen.getByRole("button", { name: "Edit normalizer.yaml" }));
    const editor = screen.getByRole("region", { name: "Edit normalizer.yaml" });
    fireEvent.change(within(editor).getByLabelText("normalizer.yaml content"), { target: { value: "scoring:\n  normalizer: normalizer/basic\n" } });
    const commit = within(editor).getByRole("button", { name: "Commit to main" });
    expect(commit).toHaveProperty("disabled", true);
    fireEvent.click(within(editor).getByRole("button", { name: "Check" }));
    await within(editor).findByText(/Checked/);
    expect(runCommand).toHaveBeenCalledWith("langpacks.edit", expect.objectContaining({ project: "demo", locale: "he-IL", sha: PACK.sha, dryRun: true }));
    fireEvent.click(commit);
    await waitFor(() => expect(runCommand).toHaveBeenLastCalledWith("langpacks.edit", expect.objectContaining({ dryRun: false, body: { files: [{ path: "normalizer.yaml", content: "scoring:\n  normalizer: normalizer/basic\n" }], message: "edit lang/he-IL/normalizer.yaml" } })));
  });

  it("edits a boost list right to left through boost.edit", async () => {
    runCommand.mockResolvedValue({ ...PACK, boost: [{ ...PACK.boost[0]!, terms: ["דוד", "שרה", "משה"] }] });
    wrap();
    const lists = screen.getByRole("list", { name: "Boost lists" });
    expect(lists.textContent).toContain("2 terms · weight 2");
    fireEvent.click(within(lists).getByRole("button", { name: "Edit boost list names" }));
    const form = screen.getByRole("form", { name: "Edit boost list names" });
    const terms = within(form).getByLabelText("Terms");
    expect(terms.getAttribute("dir")).toBe("rtl");
    fireEvent.change(terms, { target: { value: "דוד\nשרה\nמשה\n" } });
    fireEvent.click(within(form).getByRole("button", { name: "Check" }));
    await within(form).findByText("Checked: 3 terms at weight 2.");
    expect(runCommand).toHaveBeenCalledWith("boost.edit", { project: "demo", locale: "he-IL", domain: "names", sha: PACK.sha, body: { terms: ["דוד", "שרה", "משה"], weight: 2 }, dryRun: true });
    fireEvent.click(within(form).getByRole("button", { name: "Commit to main" }));
    await waitFor(() => expect(runCommand).toHaveBeenLastCalledWith("boost.edit", expect.objectContaining({ dryRun: false })));
  });
});
