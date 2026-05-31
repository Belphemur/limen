import { test, expect } from '@playwright/test'
import { readFileSync } from 'node:fs'
import {
  TENANT,
  injectTenant,
  mockSessionFetch,
  interceptAuthLogin,
  rpc,
} from '../../../shared/src/test-utils/e2e-mocks'

// Connect-RPC calls land at:
//   /t/<tenant>/api/limen.admin.v1.AdminService/<Method>         — slice 1 unimplemented
//   /t/<tenant>/api/limen.portal.v1.PortalService/<Method>       — phase 9b implemented
//   /t/<tenant>/api/limen.session.v1.SessionService/<Method>     — phase 9d
//
// The vite preview build does not ship a mock client; we intercept
// each call below.

const ADMIN_API = `/t/${TENANT}/api/`

test.describe('admin dashboard (mocked services)', () => {
  test('renders welcome, task bento and system health empty state', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    const INDEX_HTML = readFileSync('dist/index.html', 'utf-8')
    await interceptAuthLogin(page, INDEX_HTML)

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
