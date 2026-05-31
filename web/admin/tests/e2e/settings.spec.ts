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

test.describe('admin settings (mocked services)', () => {
  test('renders organization settings', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({
            settings: { name: 'Acme Corp', publicId: 'tnt_acme' },
            zitadelOrgId: '123456789',
          }),
        )
        return
      }
      await route.fulfill({ status: 404, body: `unhandled admin ${path}` })
    })

    await page.goto('/org/settings')

    await expect(
      page.getByRole('heading', { level: 1, name: 'Organization Settings' }),
    ).toBeVisible()

    await expect(page.getByTestId('section-organization')).toBeVisible()
    await expect(page.getByTestId('org-name-input')).toHaveValue('Acme Corp')
    await expect(page.getByTestId('org-public-id')).toHaveText('tnt_acme')

    await expect(page.getByTestId('section-zitadel')).toBeVisible()
    await expect(page.getByTestId('zitadel-org-id')).toHaveText('123456789')

    await expect(page.getByTestId('section-danger')).toBeVisible()
    await expect(
      page.getByRole('heading', { name: 'Danger Zone' }),
    ).toBeVisible()
  })

  test('updates organization name', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({
            settings: { name: 'Acme Corp', publicId: 'tnt_acme' },
            zitadelOrgId: '123456789',
          }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/UpdateTenantSettings') {
        await route.fulfill(
          rpc({
            settings: { name: 'New Name', publicId: 'tnt_acme' },
          }),
        )
        return
      }
      await route.fulfill({ status: 404, body: `unhandled admin ${path}` })
    })

    await page.goto('/org/settings')

    const nameInput = page.getByTestId('org-name-input')
    await nameInput.fill('')
    await nameInput.fill('New Name')

    const saveButton = page.getByTestId('org-save')
    await expect(saveButton).toBeEnabled()
    await saveButton.click()

    await expect(page.getByTestId('success-modal')).toBeVisible()
    await expect(page.getByText('Organization name updated.')).toBeVisible()
  })

  test('shows danger zone delete dialog', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({
            settings: { name: 'Acme Corp', publicId: 'tnt_acme' },
            zitadelOrgId: '123456789',
          }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/DeleteTenant') {
        await route.fulfill(rpc({}))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled admin ${path}` })
    })

    await page.goto('/org/settings')

    await page.getByTestId('danger-open').click()

    const dialog = page.getByTestId('danger-dialog')
    await expect(dialog).toBeVisible()
    await expect(page.getByTestId('danger-confirm-input')).toBeVisible()

    const confirmButton = page.getByTestId('danger-confirm')
    await expect(confirmButton).toBeDisabled()

    await page.getByTestId('danger-confirm-input').fill('wrong-id')
    await expect(confirmButton).toBeDisabled()

    await page.getByTestId('danger-confirm-input').fill('tnt_acme')
    await expect(confirmButton).toBeEnabled()

    await page.getByTestId('danger-cancel').click()
    await expect(dialog).not.toBeVisible()
  })

  test('save button is disabled when name unchanged', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({
            settings: { name: 'Acme Corp', publicId: 'tnt_acme' },
            zitadelOrgId: '123456789',
          }),
        )
        return
      }
      await route.fulfill({ status: 404, body: `unhandled admin ${path}` })
    })

    await page.goto('/org/settings')

    const saveButton = page.getByTestId('org-save')
    await expect(saveButton).toBeDisabled()

    const nameInput = page.getByTestId('org-name-input')
    await nameInput.fill('Changed Name')
    await expect(saveButton).toBeEnabled()

    await nameInput.fill('')
    await expect(saveButton).toBeDisabled()
  })
})
