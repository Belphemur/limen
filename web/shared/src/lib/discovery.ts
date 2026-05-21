// Cached fetch of GET /auth/discovery.
//
// The endpoint is small, immutable for a deployment, and called by
// every SPA shell that needs to deep-link into Zitadel. We dedupe
// concurrent calls with a module-scoped Promise so router guards and
// pages can call fetchDiscovery() without coordinating.

export interface Discovery {
  zitadelIssuer: string;
}

let cache: Promise<Discovery> | null = null;

export function fetchDiscovery(): Promise<Discovery> {
  if (cache) return cache;
  cache = (async () => {
    const res = await fetch("/auth/discovery", {
      credentials: "include",
      headers: { Accept: "application/json" },
    });
    if (!res.ok) {
      cache = null;
      throw new Error(`discovery: HTTP ${res.status}`);
    }
    return (await res.json()) as Discovery;
  })();
  return cache;
}

// resetDiscoveryCache clears the module-scoped Promise. Tests call
// this in afterEach so each spec starts from a clean slate.
export function resetDiscoveryCache(): void {
  cache = null;
}
