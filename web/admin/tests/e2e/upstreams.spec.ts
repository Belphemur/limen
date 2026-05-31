import { test, expect, type Route } from '@playwright/test'
import { readFileSync } from 'node:fs'

const INDEX_HTML = readFileSync('dist/index.html', 'utf-8')

const TENANT = 'acme'
const ADMIN_API = `/t/${TENANT}/api/`

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
    // Override fetch to mock SessionService calls with protobuf-JSON format
    await context.addInitScript(() => {
      const origFetch = window.fetch.bind(window)
      window.fetch = (input, init) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.href : 'url' in (input as Request) ? (input as Request).url : ''
        if (url.includes('/limen.session.v1.SessionService/GetSession')) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                tenant: { publicId: 'tnt_acme', name: 'Acme Corp' },
                user: { email: 'alex@acme.example', firstName: 'Alex' },
                role: 'ROLE_ADMIN',
              }),
              { status: 200, headers: { 'Content-Type': 'application/json' } },
            ),
          )
        }
        return origFetch(input, init)
      }
    })

    const state: { upstreams: Upstream[]; deleted: boolean } = {
      upstreams: [sample()],
      deleted: false,
    }

    // Intercept /auth/login redirects — serve the SPA directly so it boots
    // from the auth URL with the tenant already set, avoiding a redirect loop.
    // After the SPA boots and the guard's GetSession succeeds, redirect to
    // the return_to URL so the intended page renders.
    await page.route('**/auth/login**', async (route) => {
      if (route.request().method() === 'GET') {
        const url = new URL(route.request().url())
        const returnTo = url.searchParams.get('return_to') || '/'
        return route.fulfill({
          status: 200,
          contentType: 'text/html',
          body: INDEX_HTML.replace(
            '</head>',
            '<script>window.__LIMEN_TENANT__="acme"</script></head>',
          ).replace(
            '</body>',
            `<script>setTimeout(function(){window.location.replace(${JSON.stringify(returnTo)})},0)</script></body>`,
          ),
        })
      }
      return route.continue()
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
    await expect(page.getByRole('heading', { level: 1, name: /MCP Upstream Management/ })).toBeVisible()
    await expect(page.getByTestId('upstream-row-demo')).toBeVisible()

    // Click delete to open the ConfirmDeleteModal, then type the
    // identifier to confirm and click the confirm button.
    await page.getByTestId('upstream-delete-demo').click()
    await page.getByTestId('confirm-delete-input').fill('demo')
    await page.getByTestId('confirm-delete-confirm').click()
    await expect(page.getByTestId('upstreams-empty')).toBeVisible()
    expect(state.deleted).toBe(true)
  })

  test('add-server form submits and returns to the list', async ({ page, context }) => {
    await context.addInitScript((tenant) => {
      ;(window as Window & { __LIMEN_TENANT__?: string }).__LIMEN_TENANT__ = tenant
    }, TENANT)
    // Override fetch to mock SessionService calls with protobuf-JSON format
    await context.addInitScript(() => {
      const origFetch = window.fetch.bind(window)
      window.fetch = (input, init) => {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.href : 'url' in (input as Request) ? (input as Request).url : ''
        if (url.includes('/limen.session.v1.SessionService/GetSession')) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                tenant: { publicId: 'tnt_acme', name: 'Acme Corp' },
                user: { email: 'alex@acme.example', firstName: 'Alex' },
                role: 'ROLE_ADMIN',
              }),
              { status: 200, headers: { 'Content-Type': 'application/json' } },
            ),
          )
        }
        return origFetch(input, init)
      }
    })

    const state: { upstreams: Upstream[]; created?: Record<string, unknown> } = {
      upstreams: [],
    }

    // Intercept /auth/login redirects — serve the SPA directly so it boots
    // from the auth URL with the tenant already set, avoiding a redirect loop.
    // After the SPA boots and the guard's GetSession succeeds, redirect to
    // the return_to URL so the intended page renders.
    await page.route('**/auth/login**', async (route) => {
      if (route.request().method() === 'GET') {
        const url = new URL(route.request().url())
        const returnTo = url.searchParams.get('return_to') || '/'
        return route.fulfill({
          status: 200,
          contentType: 'text/html',
          body: INDEX_HTML.replace(
            '</head>',
            '<script>window.__LIMEN_TENANT__="acme"</script></head>',
          ).replace(
            '</body>',
            `<script>setTimeout(function(){window.location.replace(${JSON.stringify(returnTo)})},0)</script></body>`,
          ),
        })
      }
      return route.continue()
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

    // Navigate to /mcp-servers first (relative asset paths resolve correctly at
    // this depth), then client-side nav to /mcp-servers/new via the add button.
    await page.goto('/mcp-servers')
    await page.getByTestId('add-upstream').click()
    await page.getByTestId('field-display-name').fill('Demo')
    await page.getByTestId('field-name').fill('demo')
    await page.getByTestId('field-mcp-url').fill('https://example.com/mcp')
    await page.getByTestId('submit-upstream').click()

    // Dismiss the success modal to navigate back to the list.
    await page.getByTestId('success-modal-primary').click()
    await expect(page).toHaveURL(/\/mcp-servers$/)
    expect(state.created).toBeTruthy()
    expect(state.created!.identifier).toBe('demo')
  })
})
