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

// billingHeaderFetch is the cookie-bearing fetch wrapper the portal
// transport uses. On top of forwarding credentials, it peeks at the
// `X-Limen-Billing` response header the server stamps on every
// request that was served during a billing grace window and signals
// the billing store to refresh. The store import is dynamic so the
// client module stays usable in tests that haven't installed the
// billing transport yet.
const billingHeaderFetch: typeof globalThis.fetch = async (input, init) => {
  const response = await globalThis.fetch(input, { ...init, credentials: 'include' })
  if (response.headers.get('X-Limen-Billing') === 'grace') {
    // Lazy import: useBillingStore pulls in Pinia + the billing
    // client transport pin, both of which may not be configured
    // during the first test render. The store is a singleton, so
    // importing it on first signal is safe.
    void import('@limen/shared/billing').then(({ useBillingStore }) => {
      useBillingStore().handleHeaderSignal()
    })
  }
  return response
}

let cachedTransport: Transport | null = null
let cached: Client<typeof PortalService> | null = null

// portalTransport returns the process-wide cookie-bearing Connect
// transport. The same transport is reused for SessionService (see
// main.ts) so SessionService.GetSession and PortalService.* both
// hit /t/{tenant}/api/ with credentials: 'include'. It also drives
// the response-header interceptor that feeds the billing store, so
// every Connect call (PortalService + SessionService + BillingService)
// is covered without per-service plumbing.
export function portalTransport(): Transport {
  if (cachedTransport) return cachedTransport
  cachedTransport = createConnectTransport({
    baseUrl: buildBaseUrl(),
    fetch: billingHeaderFetch,
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
