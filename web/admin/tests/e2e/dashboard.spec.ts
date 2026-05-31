import { test, expect, type Route } from '@playwright/test'

// Connect-RPC calls land at:
//   /t/<tenant>/api/limen.admin.v1.AdminService/<Method>         — slice 1 unimplemented
//   /t/<tenant>/api/limen.portal.v1.PortalService/<Method>       — phase 9b implemented
//   /t/<tenant>/api/limen.session.v1.SessionService/<Method>     — phase 9d
//
// The vite preview build does not ship a mock client; we intercept
// each call below.

const TENANT = 'acme'
const ADMIN_API = `/t/${TENANT}/api/`
const SESSION_API = `/t/${TENANT}/api/limen.session.v1.SessionService/`

function rpc(body: unknown): Parameters<Route['fulfill']>[0] {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) }
}

test.describe('admin dashboard (mocked services)', () => {
  test('renders welcome, task bento and system health empty state', async ({ page, context }) => {
    await context.addInitScript((tenant) => {
      ;(window as Window & { __LIMEN_TENANT__?: string }).__LIMEN_TENANT__ = tenant
    }, TENANT)

    await page.route(`**${SESSION_API}**`, async (route) => {
      const method = route.request().url().split(SESSION_API)[1]
      if (method === 'GetSession') {
        await route.fulfill(
          rpc({
            tenant: { publicId: 'tnt_acme', name: 'Acme Corp' },
            user: { id: '', firstName: 'Alex', lastName: '', email: 'alex@acme.example' },
            role: 3,
          }),
        )
        return
      }
      await route.fulfill({ status: 404, body: `unhandled session ${method}` })
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
