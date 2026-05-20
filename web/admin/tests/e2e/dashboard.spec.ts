import { test, expect, type Route } from '@playwright/test'

// Connect-RPC calls land at /t/<tenant>/admin/api/admin.v1.AdminService/<Method>.
// The mock client wired up in src/transport/adminClient.ts answers
// every request in dev mode, so the build under `vite preview` is
// already self-contained. The route handler below is a defensive net
// that intercepts anything that escapes (e.g. when the real client
// replaces the mock in a later phase).

const TENANT = 'acme'
const API_PREFIX = `/t/${TENANT}/admin/api/admin.v1.AdminService/`

function rpc(body: unknown): Parameters<Route['fulfill']>[0] {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) }
}

test.describe('admin dashboard (mocked AdminService)', () => {
  test('renders welcome, task bento and system health empty state', async ({ page, context }) => {
    await context.addInitScript((tenant) => {
      ;(window as Window & { __LIMEN_TENANT__?: string }).__LIMEN_TENANT__ = tenant
    }, TENANT)

    await page.route(`**${API_PREFIX}**`, async (route) => {
      const method = route.request().url().split(API_PREFIX)[1]
      switch (method) {
        case 'GetSession':
          await route.fulfill(
            rpc({
              tenant: { publicId: 'tnt_acme', name: 'Acme Corp' },
              user: { firstName: 'Alex', email: 'alex@acme.example' },
              role: 'owner',
            }),
          )
          return
        case 'ListUpstreams':
          await route.fulfill(rpc({ upstreams: [] }))
          return
        case 'GetTenantSettings':
          await route.fulfill(rpc({ name: 'Acme Corp', invitedTeamAt: null, configuredAt: null }))
          return
        default:
          await route.fulfill({ status: 404, body: `unhandled ${method}` })
      }
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
