import { test, expect, type Route } from '@playwright/test'
import { readFileSync } from 'node:fs'

const INDEX_HTML = readFileSync('dist/index.html', 'utf-8')

// Connect-RPC calls land at:
//   /t/<tenant>/api/limen.admin.v1.AdminService/<Method>         — slice 1 unimplemented
//   /t/<tenant>/api/limen.portal.v1.PortalService/<Method>       — phase 9b implemented
//   /t/<tenant>/api/limen.session.v1.SessionService/<Method>     — phase 9d
//
// The vite preview build does not ship a mock client; we intercept
// each call below.

const TENANT = 'acme'
const ADMIN_API = `/t/${TENANT}/api/`

function rpc(body: unknown): Parameters<Route['fulfill']>[0] {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) }
}

test.describe('admin dashboard (mocked services)', () => {
  test('renders welcome, task bento and system health empty state', async ({ page, context }) => {
    await context.addInitScript((tenant) => {
      ;(window as Window & { __LIMEN_TENANT__?: string }).__LIMEN_TENANT__ = tenant
    }, TENANT)

    // Override fetch to intercept SessionService calls before they hit
    // the network. This runs before addInitScript boots the SPA, so all
    // Connect-RPC requests go through our mock response in protobuf JSON
    // format with the correct string-valued enum.
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
        await route.fulfill(rpc({ upstreams: [] }))
        return
      }
      // Every AdminService method stays Unimplemented for slice 1.
      if (path.startsWith('limen.admin.v1.AdminService/')) {
        await route.fulfill({
          status: 501,
          contentType: 'application/json',
          body: JSON.stringify({ code: 'unimplemented', message: 'phase 9c slice-1' }),
        })
        return
      }
      await route.fulfill({ status: 404, body: `unhandled admin ${path}` })
    })

    await page.goto('/')

    await expect(page.getByRole('heading', { level: 1, name: /Welcome to Limen/ })).toBeVisible()

    const cards = page.locator('[data-step]')
    await expect(cards).toHaveCount(3)
    await expect(page.locator('[data-step="connect"]')).toBeVisible()
    await expect(page.locator('[data-step="invite"]')).toBeVisible()
    await expect(page.locator('[data-step="configure"]')).toBeVisible()

    await expect(page.getByText('Waiting for data')).toBeVisible()
    await expect(page.getByText('0 of 3 steps completed')).toBeVisible()
  })
})
