// Connect-RPC clients for the tenant admin SPA.
//
// PortalService + AdminService share the per-tenant transport
// (cookie-bearing, baseUrl /t/{tenant}/api); SignupService rides
// a separate root-scoped transport (baseUrl /api) because the wizard
// runs before any tenant exists.
//
// All transports are cached at module scope so tests can swap them
// via set*Transport (typically with createRouterTransport from
// @connectrpc/connect).

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'

import { AdminService } from '@/gen/limen/admin/v1/admin_pb.ts'
import { PortalService } from '@/gen/limen/portal/v1/portal_pb.ts'
import { SignupService } from '@/gen/limen/signup/v1/signup_pb.ts'

function discoverTenant(): string {
  const w = window as Window & { __LIMEN_TENANT__?: string }
  if (w.__LIMEN_TENANT__) return w.__LIMEN_TENANT__
  const match = window.location.pathname.match(/^\/t\/([^/]+)\//)
  return match ? match[1] : 'dev'
}

// billingHeaderFetch wraps the cookie-bearing fetch with a peek at the
// `X-Limen-Billing` response header. The server stamps `grace` on
// every request served during a billing grace window; we forward that
// to the shared billing store so the banner appears without the user
// navigating. Lazy import keeps the module loadable in tests that
// haven't installed the billing transport pin yet.
const billingHeaderFetch: typeof globalThis.fetch = async (input, init) => {
  const response = await globalThis.fetch(input, { ...init, credentials: 'include' })
  if (response.headers.get('X-Limen-Billing') === 'grace') {
    void import('@limen/shared/billing').then(({ useBillingStore }) => {
      useBillingStore().handleHeaderSignal()
    })
  }
  return response
}

let adminTransportCache: Transport | null = null
let signupTransportCache: Transport | null = null

function buildAdminTransport(): Transport {
  return createConnectTransport({
    baseUrl: `${window.location.origin}/t/${discoverTenant()}/api`,
    fetch: billingHeaderFetch,
  })
}

// Signup transport stays on the plain cookie fetch: the wizard runs
// before any tenant exists, so the X-Limen-Billing header will never
// be stamped on its responses. Using billingHeaderFetch there would
// just add a no-op header check on every wizard call.
function buildSignupTransport(): Transport {
  return createConnectTransport({
    baseUrl: `${window.location.origin}/api`,
    fetch: (input, init) => globalThis.fetch(input, { ...init, credentials: 'include' }),
  })
}

function adminTransport(): Transport {
  if (!adminTransportCache) adminTransportCache = buildAdminTransport()
  return adminTransportCache
}

function signupTransport(): Transport {
  if (!signupTransportCache) signupTransportCache = buildSignupTransport()
  return signupTransportCache
}

export function setAdminTransport(t: Transport): void {
  adminTransportCache = t
}

export function resetAdminTransport(): void {
  adminTransportCache = null
}

export function setSignupTransport(t: Transport): void {
  signupTransportCache = t
}

export function resetSignupTransport(): void {
  signupTransportCache = null
}

// adminClient is the generated AdminService client. Lazy-built so
// tests can call setAdminTransport before the first call.
export function adminClient(): Client<typeof AdminService> {
  return createClient(AdminService, adminTransport())
}

// portalClient shares the admin transport — PortalService and
// AdminService are multiplexed on the same /t/{tenant}/api/
// mount via the http.ServeMux assembled by internal/boot/portalmount.
export function portalClient(): Client<typeof PortalService> {
  return createClient(PortalService, adminTransport())
}

// signupClient targets the root-scoped /api/ mount.
export function signupClient(): Client<typeof SignupService> {
  return createClient(SignupService, signupTransport())
}
