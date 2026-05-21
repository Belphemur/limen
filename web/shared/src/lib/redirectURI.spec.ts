import { describe, it, expect } from "vitest";
import { validateRedirectURIPattern } from "./redirectURI";

describe("validateRedirectURIPattern", () => {
  it.each([
    "https://app.acme.com/cb",
    "https://*.acme.com/cb",
    "https://acme.com:443/cb",
    "https://acme.com:*/cb/**",
    "http://localhost:3000/cb",
    "http://127.0.0.1:8080/cb",
    "com.acme.app://oauth/callback",
    "com.acme.app://oauth/*",
    "com.acme.app://oauth/**",
  ])("accepts %s", (raw) => {
    expect(validateRedirectURIPattern(raw)).toEqual({ ok: true });
  });

  it.each([
    ["", "pattern is empty"],
    ["no-scheme", "pattern is missing a scheme://"],
    ["://nohost", "pattern is missing a scheme://"],
    ["https:///cb", "missing host"],
    ["https://*.com/cb", "wildcard host needs ≥2 fixed suffix labels"],
    ["https://acme.com:abc/cb", "port must be a number or `*`"],
    ["https://acme.com/**/leaf", "`**` must be the last path segment"],
    ["https://acme.com/par*ial", "partial wildcard in path segment"],
    ["data://anything", "disallowed scheme"],
    ["JAVASCRIPT://anything", "disallowed scheme"],
    ["1bad://anything", "invalid custom scheme"],
    ["https://αcme.com/cb", "host must be ASCII (no IDN)"],
  ])("rejects %s", (raw, reasonFragment) => {
    const v = validateRedirectURIPattern(raw);
    expect(v.ok).toBe(false);
    if (!v.ok) expect(v.reason).toContain(reasonFragment);
  });
});
