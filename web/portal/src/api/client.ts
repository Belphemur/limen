import { createConnectTransport } from '@connectrpc/connect-web'
import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { PortalService } from '@gen/limen/portal/v1/portal_pb.js'

// Discover the tenant slug from the URL path. Limen mounts every
// portal asset under /t/<tenant>/portal/, so the API lives under
// /t/<tenant>/api/. In dev (vite preview / `pnpm dev`) the SPA is
// served from / and the proxy forwards /t/* to Limen — but the URL
// path still won't contain a tenant. Callers must either land via the
// Limen-served entry point or set window.__LIMEN_TENANT__ for tests.
function discoverTenant(): string {
  const w = window as Window & { __LIMEN_TENANT__?: string }
  if (w.__LIMEN_TENANT__) return w.__LIMEN_TENANT__
  const match = window.location.pathname.match(/^\/t\/([^/]+)\//)
  if (!match) {
    // Best-effort fallback so dev mode doesn't crash before login.
    return 'dev'
  }
  return match[1]
}

function buildBaseUrl(): string {
  return `${window.location.origin}/t/${discoverTenant()}/api`
}

let cachedTransport: Transport | null = null
let cached: Client<typeof PortalService> | null = null

// portalTransport returns the process-wide cookie-bearing Connect
// transport. The same transport is reused for SessionService (see
// main.ts) so SessionService.GetSession and PortalService.* both
// hit /t/{tenant}/api/ with credentials: 'include'.
export function portalTransport(): Transport {
  if (cachedTransport) return cachedTransport
  cachedTransport = createConnectTransport({
    baseUrl: buildBaseUrl(),
    fetch: (input, init) => globalThis.fetch(input, { ...init, credentials: 'include' }),
  })
  return cachedTransport
}

// portalClient returns a process-wide Connect-RPC client for the
// PortalService.
export function portalClient(): Client<typeof PortalService> {
  if (cached) return cached
  cached = createClient(PortalService, portalTransport())
  return cached
}

// resetPortalClient is exposed for tests — it forces the next call to
// portalClient() to rebuild the transport (e.g. after stubbing a base
// URL or swapping fetch).
export function resetPortalClient(): void {
  cached = null
  cachedTransport = null
}
