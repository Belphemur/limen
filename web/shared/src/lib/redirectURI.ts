// Client-side mirror of internal/oauthproxy.ValidateRedirectURIPattern.
//
// This validator exists so the Settings page can give immediate,
// per-row feedback as the admin types. The authoritative validation
// still happens on the server — the RPC will reject any pattern that
// somehow slips past us — but a JS pre-check keeps the round-trip out
// of the typing loop.
//
// Rules (kept in sync with internal/oauthproxy/uripolicy.go):
//   - non-empty
//   - "<scheme>://<rest>" with a recognised scheme
//   - http/https: host with no partial wildcards; wildcard hosts need
//     ≥2 fixed suffix labels; optional port (number or `*`); path
//     segments are literal, `*`, or `**` (only as last segment)
//   - custom schemes: reverse-DNS-shaped (^[a-z][a-z0-9+\-.]*$),
//     disallowed list rejected; path segments same rule as above
//   - http/https disallow IDN hosts (best-effort: any non-ASCII char
//     in a label is rejected)

export type RedirectURIValidation =
  | { ok: true }
  | { ok: false; reason: string };

const CUSTOM_SCHEME_RE = /^[a-z][a-z0-9+\-.]*$/;
const DISALLOWED_SCHEMES = new Set([
  "data",
  "javascript",
  "file",
  "vbscript",
  "about",
]);

export function validateRedirectURIPattern(raw: string): RedirectURIValidation {
  if (!raw) return { ok: false, reason: "pattern is empty" };

  const sep = raw.indexOf("://");
  if (sep <= 0) return { ok: false, reason: "pattern is missing a scheme://" };

  const scheme = raw.slice(0, sep).toLowerCase();
  const rest = raw.slice(sep + 3);

  if (scheme === "http" || scheme === "https") {
    return validateHTTPPattern(rest);
  }
  if (!CUSTOM_SCHEME_RE.test(scheme)) {
    return { ok: false, reason: "invalid custom scheme" };
  }
  if (DISALLOWED_SCHEMES.has(scheme)) {
    return { ok: false, reason: "disallowed scheme" };
  }
  return validatePathSegments(rest);
}

function validateHTTPPattern(rest: string): RedirectURIValidation {
  const slash = rest.indexOf("/");
  const hostport = slash === -1 ? rest : rest.slice(0, slash);
  const path = slash === -1 ? "" : rest.slice(slash + 1);
  if (!hostport) return { ok: false, reason: "missing host" };

  const colon = hostport.lastIndexOf(":");
  const host = colon === -1 ? hostport : hostport.slice(0, colon);
  const port = colon === -1 ? "" : hostport.slice(colon + 1);
  if (!host) return { ok: false, reason: "missing host" };

  const labels = host.split(".");
  let wildcard = false;
  for (const label of labels) {
    if (!label) return { ok: false, reason: "empty host label" };
    if (label === "*") {
      wildcard = true;
      continue;
    }
    if (label.includes("*")) {
      return { ok: false, reason: "partial wildcard in host label" };
    }
    for (let i = 0; i < label.length; i++) {
      if (label.charCodeAt(i) > 0x7f) {
        return { ok: false, reason: "host must be ASCII (no IDN)" };
      }
    }
  }
  if (wildcard) {
    let fixed = 0;
    for (let i = labels.length - 1; i >= 0; i--) {
      if (labels[i] === "*") break;
      fixed++;
    }
    if (fixed < 2) {
      return {
        ok: false,
        reason:
          "wildcard host needs ≥2 fixed suffix labels (e.g. `*.acme.com`)",
      };
    }
  }

  if (port) {
    if (port !== "*") {
      const n = Number(port);
      if (!Number.isInteger(n) || n < 0 || n > 65535) {
        return { ok: false, reason: "port must be a number or `*`" };
      }
    }
  }

  if (path === "") return { ok: true };
  return validatePathSegments(path);
}

function validatePathSegments(path: string): RedirectURIValidation {
  const segs = path.split("/");
  for (let i = 0; i < segs.length; i++) {
    const s = segs[i];
    if (s === "**" && i !== segs.length - 1) {
      return { ok: false, reason: "`**` must be the last path segment" };
    }
    if (s !== "*" && s !== "**" && s.includes("*")) {
      return { ok: false, reason: "partial wildcard in path segment" };
    }
  }
  return { ok: true };
}
