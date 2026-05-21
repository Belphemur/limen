import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { createRouterTransport, type Transport } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { setSessionTransport, resetSessionTransport } from '@limen/shared/session'
import { resetDiscoveryCache } from '@limen/shared'
import {
  SessionService,
  GetSessionResponseSchema,
  Role,
} from '@limen/shared/gen/limen/session/v1/session_pb.ts'
import {
  PortalService,
  ListUpstreamsResponseSchema,
  LinkState,
  type UpstreamSummary,
} from '@/gen/limen/portal/v1/portal_pb.ts'
import {
  AdminService,
  GetTenantSettingsResponseSchema,
  TenantSettingsSchema,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import Dashboard from '@/pages/Dashboard.vue'
import { setAdminTransport, resetAdminTransport } from '@/transport/adminClient'

function stubDiscovery() {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url =
      typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url
    if (url.includes('/auth/discovery')) {
      return new Response(JSON.stringify({ zitadelIssuer: 'https://idp.example' }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch in test: ${url}`)
  })
}

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

// adminAndPortalTransport stubs BOTH services on a single
// createRouterTransport — the Dashboard reuses the admin transport
// for the portal client (same /t/{tenant}/admin/api/ mount).
function adminAndPortalTransport(upstreams: Partial<UpstreamSummary>[]): Transport {
  return createRouterTransport(({ service }) => {
    service(PortalService, {
      listUpstreams: () =>
        create(ListUpstreamsResponseSchema, {
          upstreams: upstreams.map((u) => ({
            publicId: '',
            identifier: '',
            displayName: '',
            mcpUrl: '',
            strategyType: '',
            strategySubMode: '',
            requiresLink: false,
            linkState: LinkState.UNSPECIFIED,
            lastErrorReason: '',
            lastErrorAt: '',
            tools: [],
            aliases: [],
            ...u,
          })),
        }),
    })
    service(AdminService, {
      getTenantSettings: () =>
        create(GetTenantSettingsResponseSchema, {
          settings: create(TenantSettingsSchema, {
            name: 'Acme',
            publicId: 'tnt_t',
            invitedTeamAt: '',
            configuredAt: '',
          }),
          zitadelOrgId: 'z-org',
        }),
    })
  })
}

async function mountDashboard(transport: Transport) {
  setAdminTransport(transport)
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
    resetAdminTransport()
    resetSessionTransport()
    resetDiscoveryCache()
    stubDiscovery()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    resetSessionTransport()
    resetAdminTransport()
  })

  it('renders the user first name in the welcome heading', async () => {
    const wrapper = await mountDashboard(adminAndPortalTransport([]))
    const heading = wrapper.get('h1')
    expect(heading.text()).toContain('Welcome to Limen, Alex')
  })

  it('renders the three task cards and the system-health empty state', async () => {
    const wrapper = await mountDashboard(adminAndPortalTransport([]))
    const cards = wrapper.findAll('[data-step]')
    expect(cards).toHaveLength(4)
    expect(cards.map((c) => c.attributes('data-step'))).toEqual(['connect', 'ide', 'invite', 'configure'])
    expect(wrapper.text()).toContain('Waiting for data')
  })

  it('marks only Connect MCP Servers as done when a linked upstream has tools', async () => {
    const wrapper = await mountDashboard(
      adminAndPortalTransport([
        {
          publicId: 'up_1',
          identifier: 'a',
          requiresLink: true,
          linkState: LinkState.CONNECTED,
          tools: [
            {
              $typeName: 'limen.portal.v1.UpstreamTool',
              name: 't',
              description: '',
            },
          ],
        },
      ]),
    )
    expect(wrapper.text()).toContain('1 of 4 steps completed')
    expect(wrapper.text()).toContain('25%')

    const cards = wrapper.findAll('[data-step]')
    const doneFlags = cards.map((c) => c.find('[aria-label="Completed"]').exists())
    expect(doneFlags).toEqual([true, false, false, false])
  })
})
