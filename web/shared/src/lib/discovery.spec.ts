import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { fetchDiscovery, resetDiscoveryCache } from "./discovery";

describe("fetchDiscovery", () => {
  beforeEach(() => {
    resetDiscoveryCache();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    resetDiscoveryCache();
  });

  it("fetches and parses the discovery endpoint", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ zitadelIssuer: "https://idp.example" }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    const got = await fetchDiscovery();
    expect(got).toEqual({ zitadelIssuer: "https://idp.example" });
    expect(spy).toHaveBeenCalledTimes(1);
    expect(spy.mock.calls[0]?.[0]).toBe("/auth/discovery");
  });

  it("deduplicates concurrent and subsequent calls", async () => {
    const spy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ zitadelIssuer: "https://idp.example" }), {
          status: 200,
        }),
      );
    await Promise.all([fetchDiscovery(), fetchDiscovery(), fetchDiscovery()]);
    await fetchDiscovery();
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("clears the cache on a failed response so the next call retries", async () => {
    const spy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(new Response("nope", { status: 500 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ zitadelIssuer: "https://idp.example" }), {
          status: 200,
        }),
      );
    await expect(fetchDiscovery()).rejects.toThrow(/HTTP 500/);
    const second = await fetchDiscovery();
    expect(second.zitadelIssuer).toBe("https://idp.example");
    expect(spy).toHaveBeenCalledTimes(2);
  });
});
