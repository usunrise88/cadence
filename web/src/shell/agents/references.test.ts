import { describe, expect, it } from "vitest";
import { dedupeReferences, formatReference, linkifyReferences, parseReference, referenceChipLabel, referenceDoc, referenceFor, referenceFromHref, selectionReferences } from "./references";

describe("references", () => {
  it("parses the textual form with the server's kind aliases", () => {
    expect(parseReference("@mix:mix_01a")).toEqual({ kind: "mix", id: "mix_01a" });
    expect(parseReference("@eval:45#he-IL/[56,1]")).toEqual({ kind: "eval", id: "45", fragment: "he-IL/[56,1]" });
    expect(parseReference("@utt:9f3c")).toEqual({ kind: "utterance", id: "9f3c" });
    expect(parseReference("@session:ses_1")).toEqual({ kind: "agent_session", id: "ses_1" });
    expect(parseReference("@help:panels.chat")).toEqual({ kind: "help_article", id: "panels.chat" });
    expect(parseReference("mix:1")).toBeUndefined();
    expect(parseReference("@Mix:1")).toBeUndefined();
  });

  it("formats entity kinds back into short references and documents", () => {
    expect(formatReference("help_article", "panels.chat")).toBe("@help:panels.chat");
    expect(formatReference("agent_session", "ses_1")).toBe("@session:ses_1");
    expect(formatReference("mix", "mix_1", "groups/0")).toBe("@mix:mix_1#groups/0");
    expect(referenceDoc("@utt:9f3c")).toBe("utterance:9f3c");
  });

  it("turns the selection into reference chips: the document and the item selected in it", () => {
    expect(selectionReferences({ doc: "mix:mix_1", item: "groups/0" })).toEqual([{ ref: "@mix:mix_1#groups/0" }]);
    expect(selectionReferences({ doc: "run:123" }, [{ kind: "run", id: "123" }, { kind: "project", id: "demo", label: "Project demo" }])).toEqual([
      { ref: "@run:123" },
      { ref: "@project:demo", label: "Project demo" },
    ]);
    expect(referenceFor("recipe:pipelines/train.yaml")).toEqual({ ref: "@recipe:pipelines/train.yaml" });
    expect(referenceFor("broken")).toBeUndefined();
    expect(dedupeReferences([{ ref: "@a:1" }, { ref: "@a:1", label: "x" }])).toEqual([{ ref: "@a:1" }]);
    expect(referenceChipLabel({ ref: "@mix:mix_01a0efb4-639b-743e-a577-2244e71d9339" })).toBe("@mix:mix_01a0efb4-…e71d9339");
    expect(referenceChipLabel({ ref: "@run:1", label: "run one" })).toBe("run one");
  });

  it("links references in agent Markdown, but not in code or existing links", () => {
    const md = "Edited @mix:mix_1 and see @recipe:project.yaml. Also `@run:9` and [@eval:4](http://x) and\n```\n@job:1\n```";
    const out = linkifyReferences(md);
    expect(out).toContain("[@mix:mix_1](#cadence-ref=%40mix%3Amix_1)");
    expect(out).toContain("[@recipe:project.yaml](#cadence-ref=%40recipe%3Aproject.yaml).");
    expect(out).toContain("`@run:9`");
    expect(out).toContain("[@eval:4](http://x)");
    expect(out).toContain("```\n@job:1\n```");
    expect(linkifyReferences("mail me at a@b:c")).toBe("mail me at a@b:c");
    // A fence still streaming is left alone too.
    expect(linkifyReferences("```ts\nconst a = '@mix:1'")).toBe("```ts\nconst a = '@mix:1'");
  });

  it("reads a linked reference back from its href", () => {
    expect(referenceFromHref("#cadence-ref=%40mix%3Amix_1")).toBe("@mix:mix_1");
    expect(referenceFromHref("https://example.com")).toBeUndefined();
    expect(referenceFromHref(undefined)).toBeUndefined();
  });
});
