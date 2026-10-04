import { describe, expect, it } from "vitest";
import { ProblemError } from "@/api/client";
import { errorMessage } from "./commands";

const problem = (detail: string, errors?: { path: string; message: string }[]) =>
  new ProblemError({ type: "https://cadence.local/help/errors/validation-failed", title: "Validation failed", status: 422, detail, errors });

describe("errorMessage", () => {
  it("follows the generic schema detail with the field problems", () => {
    const e = problem("the request does not match the operation's schema", [{ path: "/targets/0/language", message: "sr-RS is not a language it knows" }]);
    expect(errorMessage(e)).toBe("the request does not match the operation's schema — /targets/0/language: sr-RS is not a language it knows");
  });
  it("keeps a specific detail alone (panels that list the fields render them)", () => {
    expect(errorMessage(problem("the project trained on 3 of its utterances", [{ path: "/overlaps/0", message: "x" }]))).toBe("the project trained on 3 of its utterances");
  });
  it("reads plain errors", () => {
    expect(errorMessage(new Error("boom"))).toBe("boom");
  });
});
