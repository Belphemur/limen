import { test, expect } from '@playwright/test'
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

interface ServiceAccount {
  publicId: string
  name: string
  description: string
  role: string
  tokenGeneratedAt: string
  lastUsedAt: string
  createdAt: string
  createdById: string
}

function makeServiceAccount(
  overrides: Partial<ServiceAccount> & { publicId: string; name: string },
): ServiceAccount {
  return {
    description: '',
    role: 'SERVICE_ACCOUNT_ROLE_MEMBER',
    tokenGeneratedAt: '',
    lastUsedAt: '',
    createdAt: '2026-01-01T00:00:00Z',
    createdById: 'user_admin',
    ...overrides,
  }
}

const sampleAccounts: ServiceAccount[] = [
  makeServiceAccount({
    publicId: 'sa_001',
    name: 'CI Deploy',
    description: 'CI/CD pipeline',
    role: 'SERVICE_ACCOUNT_ROLE_ADMIN',
    tokenGeneratedAt: '2026-01-15T00:00:00Z',
    lastUsedAt: '2026-05-30T00:00:00Z',
    createdAt: '2026-01-01T00:00:00Z',
    createdById: 'user_admin',
  }),
  makeServiceAccount({
    publicId: 'sa_002',
    name: 'Monitoring Bot',
    description: 'Health checks',
    role: 'SERVICE_ACCOUNT_ROLE_MEMBER',
    tokenGeneratedAt: '',
    lastUsedAt: '',
    createdAt: '2026-03-01T00:00:00Z',
    createdById: 'user_admin',
  }),
]

test.describe('admin service accounts (mocked services)', () => {
  test('renders empty state when no service accounts exist', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/ListServiceAccounts') {
        await route.fulfill(rpc({ serviceAccounts: [] }))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await page.goto('/org/service-accounts')

    await expect(page.getByRole('heading', { level: 1, name: /Service Accounts/ })).toBeVisible()
    await expect(page.getByTestId('sa-empty')).toBeVisible()
    await expect(page.getByText('No service accounts yet')).toBeVisible()
    await expect(page.getByTestId('sa-create-button')).toBeVisible()
  })

  test('lists service accounts with data', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/ListServiceAccounts') {
        await route.fulfill(rpc({ serviceAccounts: sampleAccounts }))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await page.goto('/org/service-accounts')

    await expect(page.getByTestId('sa-table')).toBeVisible()
    await expect(page.getByTestId('sa-row-sa_001')).toBeVisible()
    await expect(page.getByTestId('sa-row-sa_002')).toBeVisible()
  })

  test('creates a new service account', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    const state: { serviceAccounts: ServiceAccount[]; createdBody?: Record<string, unknown> } = {
      serviceAccounts: [],
    }

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/ListServiceAccounts') {
        await route.fulfill(rpc({ serviceAccounts: state.serviceAccounts }))
        return
      }
      if (path === 'limen.admin.v1.AdminService/CreateServiceAccount') {
        const body = (await route.request().postDataJSON()) as Record<string, unknown>
        state.createdBody = body
        const created = makeServiceAccount({
          publicId: 'sa_new',
          name: 'New Bot',
          description: 'Test',
          role: 'SERVICE_ACCOUNT_ROLE_MEMBER',
          tokenGeneratedAt: '2026-05-31T00:00:00Z',
          lastUsedAt: '',
          createdAt: '2026-05-31T00:00:00Z',
          createdById: 'user_admin',
        })
        state.serviceAccounts = [created]
        await route.fulfill(rpc({ serviceAccount: created, token: 'sa_token_abc123' }))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await page.goto('/org/service-accounts')
    await expect(page.getByTestId('sa-empty')).toBeVisible()

    await page.getByTestId('sa-create-button').click()
    await expect(page.getByTestId('sa-create-modal')).toBeVisible()

    await page.getByTestId('sa-create-name').fill('New Bot')
    await page.getByTestId('sa-create-description').fill('Test')
    await page.getByTestId('sa-create-submit').click()

    await expect(page.getByTestId('sa-token-modal')).toBeVisible()
    await page.getByTestId('sa-token-done').click()

    expect(state.createdBody).toBeTruthy()
    expect(state.createdBody!.name).toBe('New Bot')
  })

  test('deletes a service account via confirm delete modal', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    const state: { serviceAccounts: ServiceAccount[]; deleted: boolean } = {
      serviceAccounts: [sampleAccounts[0]],
      deleted: false,
    }

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/ListServiceAccounts') {
        await route.fulfill(rpc({ serviceAccounts: state.serviceAccounts }))
        return
      }
      if (path === 'limen.admin.v1.AdminService/DeleteServiceAccount') {
        state.deleted = true
        state.serviceAccounts = []
        await route.fulfill(rpc({}))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await page.goto('/org/service-accounts')
    await expect(page.getByTestId('sa-row-sa_001')).toBeVisible()

    await page.getByTestId('sa-delete-sa_001').click()
    await expect(page.getByTestId('confirm-delete-input')).toBeVisible()

    await page.getByTestId('confirm-delete-input').fill('sa_001')
    await page.getByTestId('confirm-delete-confirm').click()

    await expect(page.getByTestId('sa-empty')).toBeVisible()
    expect(state.deleted).toBe(true)
  })
})
