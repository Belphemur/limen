import { createClient, type Transport } from '@connectrpc/connect'
import type { DescService } from '@bufbuild/protobuf'

// BillingClient is the structural interface the store uses. The
// runtime value is a Connect-generated Client<typeof BillingService>
// from each SPA's per-tenant gen dir (portal/src/gen or
// admin/src/gen). The proto-generated client is structurally
// compatible with this interface — same method names, same
// request/response shapes — so the `as unknown as BillingClient`
// cast in createBillingClient below is safe.
//
// We can't use Client<typeof BillingService> directly because that
// would force shared to import from per-SPA gen dirs, which breaks
// the dependency direction (shared ← portal/admin, not the other
// way around). Re-declaring the request/response shapes here keeps
// shared self-contained while still giving the store fully-typed
// method calls.
//
// Why a transport pin instead of importing the portal client directly?
// The shared package can't reach into web/portal or web/admin to grab
// their cached client — those packages depend on shared, not the other
// way around. Pinning the transport here keeps the dependency arrow
// pointing the right direction and lets each SPA configure its own
// cookie / baseUrl / response-header interceptor without shared needing
// to know about it.

export interface GetBillingSummaryRequest {}

export interface GetBillingSummaryResponse {
  plan: string
  status: string
  activeUserCount: number
  activeSaConnectionCount: number
  stripePublishableKey: string
  currentPeriodEnd: string
  cancelAtPeriodEnd: boolean
  graceUntil: string
}

export interface CreateCheckoutSessionRequest {
  returnTo: string
}

export interface CreateCheckoutSessionResponse {
  redirectUrl: string
}

export interface OpenCustomerPortalRequest {
  returnTo: string
}

export interface OpenCustomerPortalResponse {
  redirectUrl: string
}

export interface BillingClient {
  getBillingSummary(
    request: GetBillingSummaryRequest,
  ): Promise<GetBillingSummaryResponse>
  createCheckoutSession(
    request: CreateCheckoutSessionRequest,
  ): Promise<CreateCheckoutSessionResponse>
  openCustomerPortal(
    request: OpenCustomerPortalRequest,
  ): Promise<OpenCustomerPortalResponse>
}

let cachedTransport: Transport | null = null

// setBillingTransport pins the cookie-bearing Connect transport every
// SPA must configure during boot, BEFORE the billing store or any
// composable first calls into the service. Calling it twice replaces
// the pin — useful for tests; in production it's called once from
// main.ts, right after the session transport is pinned.
export function setBillingTransport(transport: Transport): void {
  cachedTransport = transport
}

// resetBillingTransport exists for tests that need to assert the SPA
// re-binds on boot. Not used in production code.
export function resetBillingTransport(): void {
  cachedTransport = null
}

let cachedService: DescService | null = null

// setBillingService pins the proto-generated service descriptor each
// SPA holds in its own gen dir. Shared can't import the descriptor
// directly (the portal proto is per-SPA, not shared), so each SPA
// hands it in at boot. The descriptor is stored type-erased; the
// runtime client is built by createBillingClient and cast to the
// BillingClient structural interface above.
export function setBillingService<S extends DescService>(service: S): void {
  cachedService = service
}

// createBillingClient returns a Connect client bound to the SPA's
// pinned transport. Pass an explicit transport for tests that don't
// want to mutate module state.
export function createBillingClient(transport?: Transport): BillingClient {
  const t = transport ?? cachedTransport
  if (!t) {
    throw new Error(
      'billing: transport not configured — call setBillingTransport() during SPA boot',
    )
  }
  if (!cachedService) {
    throw new Error(
      'billing: service descriptor not configured — call setBillingService() during SPA boot',
    )
  }
  // Safe: the SPA injects a structurally-compatible BillingService
  // descriptor at boot, and createClient(service, transport) returns
  // a Client whose method shapes match BillingClient above.
  return createClient(cachedService, t) as unknown as BillingClient
}
