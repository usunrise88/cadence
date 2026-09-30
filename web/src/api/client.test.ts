import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { configureApiClient, onUnauthenticated, ProblemError } from "./client";
import { authLogin, projectsList, projectsNew } from "./gen/sdk.gen";

function problem(status: number, slug: string): Response {
  return new Response(JSON.stringify({ type: `https://cadence.local/help/errors/${slug}`, title: slug, status }), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}

describe("API client", () => {
  const requests: Request[] = [];
  let answer: () => Response = () => new Response(JSON.stringify({ items: [] }), { headers: { "Content-Type": "application/json" } });

  beforeAll(() => {
    configureApiClient("http://localhost/api");
  });
  afterEach(() => {
    requests.length = 0;
    vi.unstubAllGlobals();
  });
  const stubFetch = () =>
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: Request) => {
        requests.push(input);
        return answer();
      }),
    );

  it("sends the CSRF header and same-origin credentials on every request", async () => {
    stubFetch();
    await projectsList();
    await projectsNew({ body: { slug: "demo", name: "Demo" }, headers: { "Idempotency-Key": "key-12345678" } }).catch(() => undefined);
    expect(requests).toHaveLength(2);
    for (const r of requests) {
      expect(r.headers.get("Cadence-Client")).toBe("web");
      expect(r.credentials).toBe("same-origin");
      expect(r.headers.get("traceparent")).toMatch(/^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
    }
  });

  it("reports a lost session on 401, but not a failed sign-in", async () => {
    stubFetch();
    const lost = vi.fn();
    const off = onUnauthenticated(lost);
    answer = () => problem(401, "unauthenticated");
    await expect(projectsList()).rejects.toBeInstanceOf(ProblemError);
    expect(lost).toHaveBeenCalledTimes(1);
    await expect(authLogin({ body: { username: "admin", password: "x" } })).rejects.toMatchObject({ slug: "unauthenticated" });
    expect(lost).toHaveBeenCalledTimes(1);
    answer = () => problem(403, "forbidden");
    await expect(projectsList()).rejects.toMatchObject({ status: 403 });
    expect(lost).toHaveBeenCalledTimes(1);
    off();
  });
});
