import { test, expect, type Route } from '@playwright/test'

const TENANT = 'acme'
const ADMIN_API = `/t/${TENANT}/api/`
// Use regex pattern — Playwright glob `**` matching is unreliable for full URLs with ports
const SESSION_RE = /\/t\/acme\/api\/limen\.session\.v1\.SessionService\//

function rpc(body: unknown): Parameters<Route['fulfill']>[0] {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) }
}

interface Upstream {
  publicId: string
  identifier: string
  displayName: string
  mcpUrl: string
  strategyType: string
  strategySubMode: string
  requiresLink: boolean
  linkState: string
  tools: unknown[]
  aliases: string[]
}

function sample(): Upstream {
  return {
    publicId: 'up_demo',
    identifier: 'demo',
    displayName: 'Demo',
    mcpUrl: 'https://example.com/mcp',
    strategyType: 'none',
    strategySubMode: '',
    requiresLink: false,
    linkState: 'LINK_STATE_NONE',
    tools: [],
    aliases: [],
  }
}

test.describe('admin upstreams (mocked services)', () => {
  test('lists upstreams and deletes one after confirm', async ({ page, context }) => {
    await context.addInitScript((tenant) => {
      ;(window as Window & { __LIMEN_TENANT__?: string }).__LIMEN_TENANT__ = tenant
    }, TENANT)

    const state: { upstreams: Upstream[]; deleted: boolean } = {
      upstreams: [sample()],
      deleted: false,
    }

    // Intercept /auth/login redirects — serve SPA to re-boot with valid session
    await page.route('**/auth/login**', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'text/html',
          body: '<!doctype html><html><head><meta charset="UTF-8"><script>window.__LIMEN_TENANT__="acme";location.replace("/");</script></head></html>',
        })
      }
      return route.continue()
    })

    await page.route(SESSION_RE, async (route) => {
      const url = route.request().url()
      if (url.includes('/GetSession')) {
        await route.fulfill(
          rpc({
            tenant: { publicId: 'tnt_acme', name: 'Acme Corp' },
            user: { id: '', firstName: 'Alex', lastName: '', email: 'alex@acme.example' },
            role: 3,
          }),
        )
        return
      }
      await route.fulfill({ status: 404, body: 'unhandled session method' })
    })

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.portal.v1.PortalService/ListUpstreams') {
        await route.fulfill(rpc({ upstreams: state.upstreams }))
        return
      }
      if (path === 'limen.admin.v1.AdminService/DeleteUpstream') {
        state.deleted = true
        state.upstreams = []
        await route.fulfill(rpc({}))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled admin ${path}` })
    })

    await page.goto('/mcp-servers')
    await expect(page.getByRole('heading', { level: 1, name: /MCP Servers/ })).toBeVisible()
    await expect(page.getByTestId('upstream-row-demo')).toBeVisible()

    // Accept the JS confirm() dialog before clicking delete.
    page.once('dialog', (d) => d.accept())
    await page.getByTestId('upstream-delete-demo').click()
    await expect(page.getByTestId('upstreams-empty')).toBeVisible()
    expect(state.deleted).toBe(true)
  })

  test('add-server form submits and returns to the list', async ({ page, context }) => {
    await context.addInitScript((tenant) => {
      ;(window as Window & { __LIMEN_TENANT__?: string }).__LIMEN_TENANT__ = tenant
    }, TENANT)

    const state: { upstreams: Upstream[]; created?: Record<string, unknown> } = {
      upstreams: [],
    }

    // Intercept /auth/login redirects — serve SPA to re-boot with valid session
    await page.route('**/auth/login**', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'text/html',
          body: '<!doctype html><html><head><meta charset="UTF-8"><script>window.__LIMEN_TENANT__="acme";location.replace("/");</script></head></html>',
        })
      }
      return route.continue()
    })

    await page.route(SESSION_RE, async (route) => {
      const url = route.request().url()
      if (url.includes('/GetSession')) {
        await route.fulfill(
          rpc({
            tenant: { publicId: 'tnt_acme', name: 'Acme' },
            user: { id: '', firstName: 'Alex', lastName: '', email: '' },
            role: 3,
          }),
        )
        return
      }
      await route.fulfill({ status: 404, body: 'unhandled session method' })
    })

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.portal.v1.PortalService/ListUpstreams') {
        await route.fulfill(rpc({ upstreams: state.upstreams }))
        return
      }
      if (path === 'limen.admin.v1.AdminService/CreateUpstream') {
        const body = (await route.request().postDataJSON()) as Record<string, unknown>
        state.created = body
        const created = { ...sample(), identifier: String(body.identifier) }
        state.upstreams = [created]
        await route.fulfill(
          rpc({
            upstream: created,
            requiresAdminLink: false,
            connectUrl: '',
          }),
        )
        return
      }
      await route.fulfill({ status: 404, body: `unhandled admin ${path}` })
    })

    await page.goto('/mcp-servers/new')
    await page.getByTestId('field-name').fill('demo')
    await page.getByTestId('field-mcp-url').fill('https://example.com/mcp')
    await page.getByTestId('submit-upstream').click()

    await expect(page).toHaveURL(/\/mcp-servers$/)
    expect(state.created).toBeTruthy()
    expect(state.created!.identifier).toBe('demo')
  })
})
