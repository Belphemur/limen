import { test, expect, type Page } from '@playwright/test'
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

interface Member {
  userId: string
  email: string
  displayName: string
  role: string
  state: string
  lastLogin: string
}

function makeMember(overrides: Partial<Member> & { userId: string; email: string }): Member {
  return {
    displayName: overrides.email,
    role: 'MEMBER_ROLE_MEMBER',
    state: 'MEMBER_STATE_ACTIVE',
    lastLogin: '',
    ...overrides,
  }
}

async function gotoMembers(page: Page) {
  // Bootstrap at the root (1-segment URL) so relative asset paths resolve
  // correctly, then client-side navigate to /org/members.
  await page.goto('/')
  await page.getByText('Users & Roles').click()
}

test.describe('admin members (mocked services)', () => {
  test('renders members list', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    const members: Member[] = [
      makeMember({
        userId: 'user_owner',
        email: 'owner@example.com',
        displayName: 'Owner User',
        role: 'MEMBER_ROLE_OWNER',
      }),
      makeMember({
        userId: 'user_admin',
        email: 'admin@example.com',
        displayName: 'Admin User',
        role: 'MEMBER_ROLE_ADMIN',
      }),
      makeMember({
        userId: 'user_member',
        email: 'member@example.com',
        displayName: 'Member User',
        role: 'MEMBER_ROLE_MEMBER',
      }),
      makeMember({
        userId: 'user_invited',
        email: 'invited@example.com',
        displayName: 'Invited User',
        role: 'MEMBER_ROLE_MEMBER',
        state: 'MEMBER_STATE_INITIAL',
      }),
    ]

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListMembers') {
        await route.fulfill(rpc({ members }))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await gotoMembers(page)

    await expect(page.getByRole('heading', { level: 1, name: /Users & Roles/ })).toBeVisible()
    await expect(page.getByTestId('members-table')).toBeVisible()

    for (const m of members) {
      await expect(page.getByTestId(`members-row-${m.userId}`)).toBeVisible()
    }
  })

  test('filters members by role', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    const members: Member[] = [
      makeMember({
        userId: 'user_owner',
        email: 'owner@example.com',
        displayName: 'Owner User',
        role: 'MEMBER_ROLE_OWNER',
      }),
      makeMember({
        userId: 'user_admin',
        email: 'admin@example.com',
        displayName: 'Admin User',
        role: 'MEMBER_ROLE_ADMIN',
      }),
      makeMember({
        userId: 'user_member',
        email: 'member@example.com',
        displayName: 'Member User',
        role: 'MEMBER_ROLE_MEMBER',
      }),
    ]

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListMembers') {
        const body = (await route.request().postDataJSON()) as Record<string, unknown>
        const roleFilter = typeof body.roleFilter === 'string' ? body.roleFilter : ''
        const filtered =
          roleFilter === '' || roleFilter === 'MEMBER_ROLE_UNSPECIFIED'
            ? members
            : members.filter((m) => m.role === roleFilter)
        await route.fulfill(rpc({ members: filtered }))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await gotoMembers(page)
    await expect(page.getByTestId('members-row-user_owner')).toBeVisible()

    await page.getByTestId('members-role-filter').selectOption('3')
    await expect(page.getByTestId('members-row-user_owner')).toBeVisible()
    await expect(page.getByTestId('members-row-user_admin')).toBeHidden()
    await expect(page.getByTestId('members-row-user_member')).toBeHidden()
  })

  test('invites a new member', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    const members: Member[] = [
      makeMember({
        userId: 'user_owner',
        email: 'owner@example.com',
        displayName: 'Owner User',
        role: 'MEMBER_ROLE_OWNER',
      }),
    ]

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListMembers') {
        await route.fulfill(rpc({ members }))
        return
      }
      if (path === 'limen.admin.v1.AdminService/InviteMember') {
        const newMember = makeMember({
          userId: 'user_new',
          email: 'new@example.com',
          displayName: 'New User',
          role: 'MEMBER_ROLE_MEMBER',
          state: 'MEMBER_STATE_INITIAL',
        })
        members.unshift(newMember)
        await route.fulfill(rpc({ member: newMember }))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await gotoMembers(page)
    await expect(page.getByTestId('members-row-user_owner')).toBeVisible()

    await page.getByTestId('members-invite-button').click()
    await expect(page.getByTestId('members-invite-modal')).toBeVisible()

    await page.getByTestId('members-invite-email').fill('new@example.com')
    await page.getByTestId('members-invite-given-name').fill('New')
    await page.getByTestId('members-invite-family-name').fill('User')
    await page.getByTestId('members-invite-submit').click()

    await expect(page.getByTestId('members-row-user_new')).toBeVisible()
  })

  test('edits a member role', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    const members: Member[] = [
      makeMember({
        userId: 'user_owner',
        email: 'owner@example.com',
        displayName: 'Owner User',
        role: 'MEMBER_ROLE_OWNER',
      }),
      makeMember({
        userId: 'user_member',
        email: 'member@example.com',
        displayName: 'Member User',
        role: 'MEMBER_ROLE_MEMBER',
      }),
    ]

    let updateRoleCalled = false

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListMembers') {
        await route.fulfill(rpc({ members }))
        return
      }
      if (path === 'limen.admin.v1.AdminService/UpdateMemberRole') {
        updateRoleCalled = true
        const body = (await route.request().postDataJSON()) as Record<string, unknown>
        const target = members.find((m) => m.userId === body.userId)
        if (target) target.role = String(body.role)
        await route.fulfill(rpc({}))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await gotoMembers(page)
    await expect(page.getByTestId('members-row-user_member')).toBeVisible()

    await page.getByTestId('members-edit-user_member').click()
    await expect(page.getByTestId('members-edit-modal')).toBeVisible()

    await page.getByTestId('members-edit-role').selectOption('2')
    await page.getByTestId('members-edit-submit').click()

    await expect(page.getByTestId('members-edit-modal')).toBeHidden()
    expect(updateRoleCalled).toBe(true)
  })

  test('removes a member', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    const members: Member[] = [
      makeMember({
        userId: 'user_owner',
        email: 'owner@example.com',
        displayName: 'Owner User',
        role: 'MEMBER_ROLE_OWNER',
      }),
      makeMember({
        userId: 'user_member',
        email: 'member@example.com',
        displayName: 'Member User',
        role: 'MEMBER_ROLE_MEMBER',
      }),
    ]

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListMembers') {
        await route.fulfill(rpc({ members }))
        return
      }
      if (path === 'limen.admin.v1.AdminService/RemoveMember') {
        const body = (await route.request().postDataJSON()) as Record<string, unknown>
        const idx = members.findIndex((m) => m.userId === body.userId)
        if (idx !== -1) members.splice(idx, 1)
        await route.fulfill(rpc({}))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await gotoMembers(page)
    await expect(page.getByTestId('members-row-user_member')).toBeVisible()

    await page.getByTestId('members-remove-user_member').click()
    await expect(page.getByTestId('error-modal-primary')).toBeVisible()
    await expect(page.getByTestId('error-modal-primary')).toHaveText('Remove')

    await page.getByTestId('error-modal-primary').click()
    await expect(page.getByTestId('members-row-user_member')).toBeHidden()
  })

  test('prevents removing the last owner', async ({ page, context }) => {
    await injectTenant(context)
    await mockSessionFetch(context)
    await interceptAuthLogin(page, INDEX_HTML)

    const members: Member[] = [
      makeMember({
        userId: 'user_owner',
        email: 'owner@example.com',
        displayName: 'Owner User',
        role: 'MEMBER_ROLE_OWNER',
      }),
    ]

    await page.route(`**${ADMIN_API}**`, async (route) => {
      const path = route.request().url().split(ADMIN_API)[1]
      if (path === 'limen.admin.v1.AdminService/GetTenantSettings') {
        await route.fulfill(
          rpc({ settings: { name: 'Acme Corp', publicId: 'tnt_acme' }, zitadelOrgId: '12345' }),
        )
        return
      }
      if (path === 'limen.admin.v1.AdminService/ListMembers') {
        await route.fulfill(rpc({ members }))
        return
      }
      await route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    await gotoMembers(page)
    await expect(page.getByTestId('members-row-user_owner')).toBeVisible()

    const removeBtn = page.getByTestId('members-remove-user_owner')
    await expect(removeBtn).toBeDisabled()
  })
})
