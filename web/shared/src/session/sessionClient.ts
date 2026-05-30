import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { SessionService } from '@gen/limen/session/v1/session_pb.ts'

// SessionClient is just the Connect-generated client typed against the
// shared service. Each SPA hands in its own Transport (cookie-bearing
// fetch, tenant-derived baseUrl); the store and router guard then call
// methods on the resulting client without caring how the transport was
// built.

export type SessionClient = Client<typeof SessionService>

let cachedTransport: Transport | null = null

// setSessionTransport pins the cookie-bearing Connect transport every
// SPA must configure during boot, BEFORE the router guard runs (the
// guard calls into the session store, which calls into the client,
// which reads this transport). Calling it twice replaces the pin —
// useful for tests; in production it's called once from main.ts.
export function setSessionTransport(transport: Transport): void {
  cachedTransport = transport
}

// resetSessionTransport exists for tests that need to assert the SPA
// re-binds on boot. Not used in production code.
export function resetSessionTransport(): void {
  cachedTransport = null
}

// createSessionClient returns a Connect client bound to the SPA's
// pinned transport. Pass an explicit transport for tests that don't
// want to mutate module state.
export function createSessionClient(transport?: Transport): SessionClient {
  const t = transport ?? cachedTransport
  if (!t) {
    throw new Error(
      'session: transport not configured — call setSessionTransport() during SPA boot',
    )
  }
  return createClient(SessionService, t)
}
