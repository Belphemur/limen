// Hand-written interface for the admin RPCs the SPA needs while
// proto/limen/admin/v1/admin.proto is being authored. When the proto
// lands, this module is replaced by a thin wrapper around the
// generated AdminService client.
//
// Session identity is owned by limen.session.v1.SessionService; this
// module no longer carries a getSession() method. Consumers must
// read the current tenant/user/role from @limen/shared/session.
//
// TODO(phase-9c-proto): wire to generated AdminService client when proto lands.

export type Role = 'owner' | 'admin' | 'member'

export interface UpstreamRow {
  id: string
  name: string
  status: 'pending_catalog' | 'ready' | 'error'
  toolCount: number
}

export interface ListUpstreamsResponse {
  upstreams: UpstreamRow[]
}

export interface TenantSettings {
  name: string
  invitedTeamAt: string | null
  configuredAt: string | null
}

export interface AdminClient {
  listUpstreams(): Promise<ListUpstreamsResponse>
  getTenantSettings(): Promise<TenantSettings>
  markInvitedTeam(): Promise<void>
}

// Dev / preview fixtures. Kept gated behind import.meta.env.DEV so a
// production bundle that accidentally reaches this module crashes
// loudly instead of serving stale fake data.
const DEV_FIXTURES = {
  upstreams: { upstreams: [] as UpstreamRow[] },
  settings: {
    name: 'Acme Corp',
    invitedTeamAt: null as string | null,
    configuredAt: null as string | null,
  },
}

class MockAdminClient implements AdminClient {
  // TODO(phase-9c-proto): replace every method with a generated stub call.
  async listUpstreams(): Promise<ListUpstreamsResponse> {
    return Promise.resolve(DEV_FIXTURES.upstreams)
  }
  async getTenantSettings(): Promise<TenantSettings> {
    return Promise.resolve({ ...DEV_FIXTURES.settings })
  }
  async markInvitedTeam(): Promise<void> {
    DEV_FIXTURES.settings.invitedTeamAt = new Date().toISOString()
    return Promise.resolve()
  }
}

let cached: AdminClient | null = null

export function adminClient(): AdminClient {
  if (cached) return cached
  if (import.meta.env.DEV || import.meta.env.MODE === 'test') {
    cached = new MockAdminClient()
    return cached
  }
  // TODO(phase-9c-proto): construct the real Connect-RPC client here.
  // Until the proto lands, refuse to serve production traffic with
  // canned fixtures — fail loudly instead.
  throw new Error(
    'AdminClient is not configured: build is missing the generated AdminService client (phase-9c-proto).',
  )
}

export function resetAdminClient(): void {
  cached = null
}

export function setAdminClient(client: AdminClient): void {
  cached = client
}
