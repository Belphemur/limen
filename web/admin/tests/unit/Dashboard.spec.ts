import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { createRouterTransport, type Transport } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import {
  setSessionTransport,
  resetSessionTransport,
} from '@limen/shared/session'
import {
  SessionService,
  GetSessionResponseSchema,
  Role,
} from '@limen/shared/gen/limen/session/v1/session_pb.ts'
import Dashboard from '@/pages/Dashboard.vue'
import {
  setAdminClient,
  resetAdminClient,
  type AdminClient,
  type ListUpstreamsResponse,
  type TenantSettings,
} from '@/transport/adminClient'

function buildRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/mcp-servers/new', component: { template: '<div />' } },
      { path: '/org/settings', component: { template: '<div />' } },
    ],
  })
}

function makeClient(overrides: Partial<AdminClient> = {}): AdminClient {
  const base: AdminClient = {
    listUpstreams: (): Promise<ListUpstreamsResponse> => Promise.resolve({ upstreams: [] }),
    getTenantSettings: (): Promise<TenantSettings> =>
      Promise.resolve({ name: 'Acme', invitedTeamAt: null, configuredAt: null }),
    markInvitedTeam: () => Promise.resolve(),
  }
  return { ...base, ...overrides }
}

function happySessionTransport(): Transport {
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () =>
        create(GetSessionResponseSchema, {
          tenant: { publicId: 'tnt_t', name: 'Acme' },
          user: { id: 'usr_1', email: 'alex@acme.example', firstName: 'Alex', lastName: 'Doe' },
          role: Role.OWNER,
        }),
    })
  })
}

async function mountDashboard(client: AdminClient) {
  setAdminClient(client)
  setSessionTransport(happySessionTransport())
  const router = buildRouter()
  await router.push('/')
  await router.isReady()
  const wrapper = mount(Dashboard, { global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

describe('Dashboard', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    resetAdminClient()
    resetSessionTransport()
  })

  afterEach(() => {
    resetSessionTransport()
  })

  it('renders the user first name in the welcome heading', async () => {
    const wrapper = await mountDashboard(makeClient())
    const heading = wrapper.get('h1')
    expect(heading.text()).toContain('Welcome to Limen, Alex')
  })

  it('renders the three task cards and the system-health empty state', async () => {
    const wrapper = await mountDashboard(makeClient())
    const cards = wrapper.findAll('[data-step]')
    expect(cards).toHaveLength(3)
    expect(cards.map((c) => c.attributes('data-step'))).toEqual(['connect', 'invite', 'configure'])
    expect(wrapper.text()).toContain('Waiting for data')
  })

  it('marks only Connect MCP Servers as done when a ready upstream exists', async () => {
    const wrapper = await mountDashboard(
      makeClient({
        listUpstreams: () =>
          Promise.resolve({
            upstreams: [{ id: 'up_1', name: 'a', status: 'ready', toolCount: 4 }],
          }),
      }),
    )
    expect(wrapper.text()).toContain('1 of 3 steps completed')
    expect(wrapper.text()).toContain('33%')

    const cards = wrapper.findAll('[data-step]')
    const doneFlags = cards.map((c) => c.find('[aria-label="Completed"]').exists())
    expect(doneFlags).toEqual([true, false, false])
  })

  it('marks every step done when all three signals are present', async () => {
    const wrapper = await mountDashboard(
      makeClient({
        listUpstreams: () =>
          Promise.resolve({
            upstreams: [{ id: 'up_1', name: 'a', status: 'ready', toolCount: 1 }],
          }),
        getTenantSettings: () =>
          Promise.resolve({
            name: 'Acme',
            invitedTeamAt: '2026-01-01T00:00:00Z',
            configuredAt: '2026-01-01T00:00:00Z',
          }),
      }),
    )
    expect(wrapper.text()).toContain('3 of 3 steps completed')
    expect(wrapper.text()).toContain('100%')
  })
})
