import { test, expect, type Page, type BrowserContext, type Route } from '@playwright/test'
import { readFileSync } from 'node:fs'
import {
  TENANT,
  injectTenant,
  mockSessionFetch,
  interceptAuthLogin,
  rpc,
} from '../../../shared/src/test-utils/e2e-mocks'

const ADMIN_API = `/t/${TENANT}/api/`
const INDEX_HTML = readFileSync('dist/index.html', 'utf-8')

const SA = {
  publicId: 'sa_001',
  name: 'CI Deploy',
  description: 'CI/CD pipeline account',
  role: 'SERVICE_ACCOUNT_ROLE_ADMIN',
  tokenGeneratedAt: '2026-01-15T00:00:00Z',
  lastUsedAt: '2026-05-30T00:00:00Z',
  createdAt: '2026-01-01T00:00:00Z',
  createdById: 'user_admin',
}

const UPSTREAMS = {
  upstreams: [
    { publicId: 'up_001', identifier: 'github', displayName: 'GitHub', mcpUrl: 'https://api.github.com/mcp', strategyType: 'mcp_spec', strategySubMode: '', requiresLink: true, tools: [{ name: 'search_code' }], aliases: [] },
    { publicId: 'up_002', identifier: 'sentry', displayName: 'Sentry', mcpUrl: 'https://sentry.io/mcp', strategyType: 'static_header', strategySubMode: 'byok', requiresLink: true, tools: [], aliases: [] },
  ],
}

const LINKS = {
  links: [
    { upstreamPublicId: 'up_001', enabled: true },
  ],
}

/**
 * Sets up common mocks and interceptors for every service-account detail test.
 */
async function bootstrap(page: Page, context: BrowserContext) {
  await injectTenant(context)
  await mockSessionFetch(context)
  await interceptAuthLogin(page, INDEX_HTML)
}

/**
 * Creates a page.route handler that mocks all RPC calls needed for the
 * service-account detail page (both AdminService and PortalService).
 *
 * The `overrides` callback receives the intercepted path and can return
 * a custom response body, or `undefined` to fall through to the default
 * handler.
 */
function createRouteHandler(overrides?: (path: string) => Promise<unknown | undefined>) {
  return async (route: Route) => {
    const path = route.request().url().split(ADMIN_API)[1]

    if (overrides) {
      const custom = await overrides(path)
      if (custom !== undefined) {
        await route.fulfill(rpc(custom))
        return
      }
    }

    // Default responses
    if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
      await route.fulfill(rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }))
      return
    }
    if (path === 'limen.admin.v1.AdminService/ListServiceAccounts') {
      await route.fulfill(rpc({ serviceAccounts: [SA] }))
      return
    }
    if (path === 'limen.admin.v1.AdminService/GetServiceAccount') {
      await route.fulfill(rpc({ serviceAccount: SA }))
      return
    }
    if (path === 'limen.admin.v1.AdminService/ListServiceAccountUpstreamLinks') {
      await route.fulfill(rpc(LINKS))
      return
    }
    if (path === 'limen.portal.v1.PortalService/ListUpstreams') {
      await route.fulfill(rpc(UPSTREAMS))
      return
    }
    if (path === 'limen.admin.v1.AdminService/UpdateServiceAccount') {
      await route.fulfill(rpc({ serviceAccount: SA }))
      return
    }
    if (path === 'limen.admin.v1.AdminService/RegenerateServiceAccountToken') {
      await route.fulfill(rpc({ token: 'new_token_xyz' }))
      return
    }
    if (path === 'limen.admin.v1.AdminService/DeleteServiceAccount') {
      await route.fulfill(rpc({}))
      return
    }

    await route.fulfill({ status: 404, body: `unhandled: ${path}` })
  }
}

/**
 * Navigate to the service-accounts list, then click the named account
 * to reach the detail page via client-side routing.
 */
async function navigateToDetail(page: Page, name: string) {
  await page.goto('/org/service-accounts')
  await expect(page.getByRole('heading', { level: 1, name: /Service Accounts/ })).toBeVisible()
  await page.getByText(name).first().click()
  await expect(page).toHaveURL(/\/org\/service-accounts\/sa_001/)
}

test.describe('admin service account detail (mocked services)', () => {
  test('renders service account detail', async ({ page, context }) => {
    await bootstrap(page, context)
    await page.route(`**${ADMIN_API}**`, createRouteHandler())

    await navigateToDetail(page, 'CI Deploy')

    await expect(page.getByRole('heading', { level: 1, name: /CI Deploy/ })).toBeVisible()
    await expect(page.getByTestId('field-sa-name')).toHaveValue('CI Deploy')
    await expect(page.getByTestId('field-sa-description')).toHaveValue('CI/CD pipeline account')
    await expect(page.getByRole('heading', { level: 2, name: /MCP Portal/ })).toBeVisible()
    await expect(page.getByRole('heading', { level: 2, name: /Danger zone/ })).toBeVisible()
  })

  test('edits service account name and description', async ({ page, context }) => {
    await bootstrap(page, context)

    let getServiceAccountCallCount = 0
    const updatedSA = { ...SA, name: 'Updated Bot', description: 'Updated desc' }

    await page.route(
      `**${ADMIN_API}**`,
      createRouteHandler(async (path) => {
        if (path === 'limen.admin.v1.AdminService/GetServiceAccount') {
          getServiceAccountCallCount++
          // Return updated data after the first call (which happens on mount)
          if (getServiceAccountCallCount > 1) {
            return { serviceAccount: updatedSA }
          }
          return { serviceAccount: SA }
        }
        if (path === 'limen.admin.v1.AdminService/UpdateServiceAccount') {
          return { serviceAccount: updatedSA }
        }
        return undefined
      }),
    )

    await navigateToDetail(page, 'CI Deploy')

    await expect(page.getByTestId('field-sa-name')).toHaveValue('CI Deploy')
    await expect(page.getByTestId('field-sa-description')).toHaveValue('CI/CD pipeline account')

    await page.getByTestId('field-sa-name').fill('Updated Bot')
    await page.getByTestId('field-sa-description').fill('Updated desc')

    await expect(page.getByTestId('save-sa')).toBeEnabled()
    await page.getByTestId('save-sa').click()

    // After save, the page re-fetches the account and should show updated values
    await expect(page.getByTestId('field-sa-name')).toHaveValue('Updated Bot')
    await expect(page.getByTestId('field-sa-description')).toHaveValue('Updated desc')
  })

  test('shows upstream links section with data', async ({ page, context }) => {
    await bootstrap(page, context)
    await page.route(`**${ADMIN_API}**`, createRouteHandler())

    await navigateToDetail(page, 'CI Deploy')

    await expect(page.getByRole('heading', { level: 2, name: /MCP Portal/ })).toBeVisible()
    await expect(page.getByText('GitHub', { exact: true })).toBeVisible()
    await expect(page.getByText('Sentry', { exact: true })).toBeVisible()
  })

  test('opens delete confirmation modal', async ({ page, context }) => {
    await bootstrap(page, context)
    await page.route(`**${ADMIN_API}**`, createRouteHandler())

    await navigateToDetail(page, 'CI Deploy')

    await page.getByTestId('delete-sa').click()
    await expect(page.getByTestId('confirm-delete-input')).toBeVisible()

    await page.getByTestId('confirm-delete-cancel').click()
    await expect(page.getByTestId('confirm-delete-input')).toBeHidden()
  })
})
