import { createConnectTransport } from '@connectrpc/connect-web'
import type { Transport } from '@connectrpc/connect'

// Discover the tenant slug from the URL path. The admin SPA is
// mounted under /t/<tenant>/admin/ in production. In dev / tests we
// fall back to window.__LIMEN_TENANT__ or "dev".
function discoverTenant(): string {
  const w = window as Window & { __LIMEN_TENANT__?: string }
  if (w.__LIMEN_TENANT__) return w.__LIMEN_TENANT__
  const match = window.location.pathname.match(/^\/t\/([^/]+)\//)
  return match ? match[1] : 'dev'
}

function buildBaseUrl(): string {
  return `${window.location.origin}/t/${discoverTenant()}/admin/api`
}

let cached: Transport | null = null

// adminTransport returns a process-wide Connect-RPC transport for
// AdminService. Cookie-based session means credentials must be sent
// on every fetch.
export function adminTransport(): Transport {
  if (cached) return cached
  cached = createConnectTransport({
    baseUrl: buildBaseUrl(),
    fetch: (input, init) => globalThis.fetch(input, { ...init, credentials: 'include' }),
  })
  return cached
}

export function resetAdminTransport(): void {
  cached = null
}
