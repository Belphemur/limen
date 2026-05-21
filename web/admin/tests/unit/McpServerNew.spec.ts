import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { createRouterTransport, type Transport } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import {
  AdminService,
  CreateUpstreamResponseSchema,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import {
  PortalService,
  UpstreamSummarySchema,
} from '@/gen/limen/portal/v1/portal_pb.ts'
import McpServerNew from '@/pages/McpServerNew.vue'
import { setAdminTransport, resetAdminTransport } from '@/transport/adminClient'

function buildRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/mcp-servers', component: { template: '<div />' } },
      { path: '/mcp-servers/new', component: { template: '<div />' } },
    ],
  })
}

function transport(captured: { req?: unknown }): Transport {
  return createRouterTransport(({ service }) => {
    service(AdminService, {
      createUpstream: (req) => {
        captured.req = req
        return create(CreateUpstreamResponseSchema, {
          upstream: create(UpstreamSummarySchema, {
            publicId: 'up_new',
            name: req.name,
            displayName: req.displayName,
            mcpUrl: req.mcpUrl,
            strategyType: req.strategyType,
          }),
          requiresAdminLink: false,
          connectUrl: '',
        })
      },
    })
    service(PortalService, {})
  })
}

async function mountPage(t: Transport) {
  setAdminTransport(t)
  const router = buildRouter()
  await router.push('/mcp-servers/new')
  await router.isReady()
  const wrapper = mount(McpServerNew, { global: { plugins: [router] } })
  await flushPromises()
  return { wrapper, router }
}

describe('McpServerNew', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    resetAdminTransport()
  })
  afterEach(() => resetAdminTransport())

  it('submits a none-strategy upstream and navigates back to the list', async () => {
    const captured: { req?: { name: string; strategyType: string; mcpUrl: string } } = {}
    const { wrapper, router } = await mountPage(transport(captured as { req?: unknown }))

    await wrapper.get('[data-testid="field-display-name"]').setValue('Demo')
    await wrapper.get('[data-testid="field-mcp-url"]').setValue('https://example.com/mcp')
    await wrapper.get('[data-testid="upstream-new-form"]').trigger('submit.prevent')
    await flushPromises()

    expect(captured.req).toBeTruthy()
    expect(captured.req!.name).toBe('demo')
    expect(captured.req!.strategyType).toBe('none')
    // Success modal is teleported to document.body.
    const primary = document.querySelector<HTMLButtonElement>(
      '[data-testid="success-modal-primary"]',
    )
    expect(primary).toBeTruthy()
    primary!.click()
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/mcp-servers')
  })

  it('switching to static_header reveals the header fields', async () => {
    const captured = {}
    const { wrapper } = await mountPage(transport(captured))
    await wrapper.get('[data-testid="strategy-static-header"]').setValue()
    await flushPromises()
    expect(wrapper.find('[data-testid="field-api-key"]').exists()).toBe(true)
  })

  it('disables submit when the JSON editor is invalid', async () => {
    const { wrapper } = await mountPage(transport({}))
    await wrapper.get('[data-testid="field-display-name"]').setValue('Demo')
    await wrapper.get('[data-testid="field-mcp-url"]').setValue('https://example.com/mcp')
    await wrapper.get('[data-testid="context-json-editor"]').setValue('{bad')
    await flushPromises()
    const btn = wrapper.get('[data-testid="submit-upstream"]')
    expect((btn.element as HTMLButtonElement).disabled).toBe(true)
  })
})
