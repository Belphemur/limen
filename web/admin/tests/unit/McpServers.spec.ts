import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { createRouterTransport, type Transport } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import {
  PortalService,
  ListUpstreamsResponseSchema,
  LinkState,
  type UpstreamSummary,
} from '@/gen/limen/portal/v1/portal_pb.ts'
import {
  AdminService,
  DeleteUpstreamResponseSchema,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import McpServers from '@/pages/McpServers.vue'
import {
  setAdminTransport,
  resetAdminTransport,
} from '@/transport/adminClient'

function buildRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/mcp-servers', component: { template: '<div />' } },
      { path: '/mcp-servers/new', component: { template: '<div />' } },
      { path: '/mcp-servers/:id', component: { template: '<div />' } },
    ],
  })
}

function withUpstreams(
  upstreams: Partial<UpstreamSummary>[],
  opts?: { onDelete?: () => void },
): Transport {
  return createRouterTransport(({ service }) => {
    service(PortalService, {
      listUpstreams: () =>
        create(ListUpstreamsResponseSchema, {
          upstreams: upstreams.map((u) => ({
            publicId: 'up_x',
            name: 'x',
            displayName: '',
            mcpUrl: '',
            strategyType: 'none',
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
      deleteUpstream: () => {
        opts?.onDelete?.()
        return create(DeleteUpstreamResponseSchema, {})
      },
    })
  })
}

async function mountPage(transport: Transport) {
  setAdminTransport(transport)
  const router = buildRouter()
  await router.push('/mcp-servers')
  await router.isReady()
  const wrapper = mount(McpServers, { global: { plugins: [router] } })
  await flushPromises()
  return { wrapper, router }
}

describe('McpServers', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    resetAdminTransport()
  })
  afterEach(() => resetAdminTransport())

  it('shows the empty state when there are no upstreams', async () => {
    const { wrapper } = await mountPage(withUpstreams([]))
    expect(wrapper.find('[data-testid="upstreams-empty"]').exists()).toBe(true)
  })

  it('renders one row per upstream', async () => {
    const { wrapper } = await mountPage(
      withUpstreams([
        { publicId: 'up_a', name: 'github', displayName: 'GitHub', mcpUrl: 'https://github.com/mcp' },
        { publicId: 'up_b', name: 'jira', mcpUrl: 'https://acme.atlassian.com/mcp' },
      ]),
    )
    expect(wrapper.find('[data-testid="upstream-row-github"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="upstream-row-jira"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('GitHub')
  })

  it('calls deleteUpstream after confirmation and drops the row', async () => {
    let called = false
    const origConfirm = window.confirm
    window.confirm = () => true
    try {
      const { wrapper } = await mountPage(
        withUpstreams(
          [{ publicId: 'up_a', name: 'github', displayName: 'GitHub' }],
          { onDelete: () => (called = true) },
        ),
      )
      await wrapper.get('[data-testid="upstream-delete-github"]').trigger('click')
      await flushPromises()
      expect(called).toBe(true)
      expect(wrapper.find('[data-testid="upstreams-empty"]').exists()).toBe(true)
    } finally {
      window.confirm = origConfirm
    }
  })
})
