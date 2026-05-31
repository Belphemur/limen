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

const SAMPLE_PRESETS = {
  presets: [
    {
      key: 'vscode',
      displayName: 'VS Code',
      icon: 'code',
      patterns: ['http://localhost:*', 'vscode://*'],
    },
    {
      key: 'cursor',
      displayName: 'Cursor',
      icon: 'sparkles',
      patterns: ['http://localhost:*', 'cursor://*'],
    },
    {
      key: 'claude',
      displayName: 'Claude Code',
      icon: 'terminal',
      patterns: ['http://localhost:*'],
    },
    {
      key: 'copilot',
      displayName: 'GitHub Copilot',
      icon: 'bot',
      patterns: ['http://localhost:*', 'vscode://*'],
    },
  ],
}

const SAMPLE_ENTRIES = {
  entries: [
    { publicId: 'entry_1', ideKey: 'vscode', label: 'VS Code', pattern: 'http://localhost:*' },
    { publicId: 'entry_2', ideKey: 'vscode', label: 'VS Code', pattern: 'vscode://*' },
    { publicId: 'entry_3', ideKey: '', label: 'Custom', pattern: 'https://myapp.com/callback' },
  ],
}

test.describe('admin IDE configuration (mocked services)', () => {
  test('renders IDE configuration page with gateway URL', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListIDEPresets') {
        await route.fulfill(rpc(SAMPLE_PRESETS))
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListAllowlistEntries') {
        await route.fulfill(rpc(SAMPLE_ENTRIES))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await page.goto('/org/ide-configuration')

    await expect(page.getByRole('heading', { level: 1, name: /IDE Configuration/ })).toBeVisible()
    await expect(page.getByTestId('section-gateway-url')).toBeVisible()
    await expect(page.getByTestId('gateway-url-value')).toContainText('/mcp')
    await expect(page.getByTestId('section-portal-url')).toBeVisible()
    await expect(page.getByTestId('section-allowlist')).toBeVisible()
    await expect(page.getByTestId('section-examples')).toBeVisible()
  })

  test('renders IDE allowlist manager with presets', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListIDEPresets') {
        await route.fulfill(rpc(SAMPLE_PRESETS))
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListAllowlistEntries') {
        await route.fulfill(rpc(SAMPLE_ENTRIES))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await page.goto('/org/ide-configuration')

    await expect(page.getByTestId('ide-allowlist-manager')).toBeVisible()
    await expect(page.getByTestId('ide-preset-vscode')).toBeVisible()
    await expect(page.getByTestId('ide-preset-vscode')).toContainText('active')
    await expect(page.getByTestId('ide-preset-claude')).toBeVisible()
    await expect(page.getByTestId('ide-preset-claude')).toContainText('inactive')
  })

  test('shows custom URI empty state', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListIDEPresets') {
        await route.fulfill(rpc(SAMPLE_PRESETS))
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListAllowlistEntries') {
        await route.fulfill(
          rpc({
            entries: [
              { publicId: 'e1', ideKey: 'vscode', label: 'VS Code', pattern: 'http://localhost:*' },
            ],
          }),
        )
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await page.goto('/org/ide-configuration')

    await expect(page.getByTestId('custom-empty')).toBeVisible()
  })

  test('clicking add URI opens editor modal', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListIDEPresets') {
        await route.fulfill(rpc(SAMPLE_PRESETS))
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListAllowlistEntries') {
        await route.fulfill(rpc(SAMPLE_ENTRIES))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await page.goto('/org/ide-configuration')

    await page.getByTestId('custom-add').click()
    await expect(page.getByTestId('custom-editor')).toBeVisible()

    await page.getByTestId('custom-label-input').fill('Test URI')
    await page.getByTestId('custom-pattern-input').fill('https://test.com/callback')

    await page.getByTestId('custom-cancel').click()
    await expect(page.getByTestId('custom-editor')).toBeHidden()
  })
})
