import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { BillingService } from '@shared-gen/limen/portal/v1/portal_pb.ts'

// BillingClient is just the Connect-generated client typed against the
// shared billing service. Each SPA hands in its own Transport (the same
// cookie-bearing /t/{tenant}/api/ transport used for PortalService);
// the store and any composables then call methods on the resulting
// client without caring how the transport was built.
//
// Why a transport pin instead of importing the portal client directly?
// The shared package can't reach into web/portal or web/admin to grab
// their cached client — those packages depend on shared, not the other
// way around. Pinning the transport here keeps the dependency arrow
// pointing the right direction and lets each SPA configure its own
// cookie / baseUrl / response-header interceptor without shared needing
// to know about it.

export type BillingClient = Client<typeof BillingService>

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
  return createClient(BillingService, t)
}
