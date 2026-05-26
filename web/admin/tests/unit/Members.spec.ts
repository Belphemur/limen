import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createRouterTransport } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { resetDiscoveryCache } from '@limen/shared'
import {
  AdminService,
  GetTenantSettingsResponseSchema,
  InviteMemberResponseSchema,
  ListMembersResponseSchema,
  MemberRole,
  MemberSchema,
  MemberState,
  RemoveMemberResponseSchema,
  TenantSettingsSchema,
  UpdateMemberRoleResponseSchema,
  type InviteMemberRequest,
  type ListMembersRequest,
  type RemoveMemberRequest,
  type UpdateMemberRoleRequest,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import Members from '@/pages/Members.vue'
import { resetAdminTransport, setAdminTransport } from '@/transport/adminClient'

interface Spies {
  list: ReturnType<typeof vi.fn<(req: ListMembersRequest) => void>>
  invite: ReturnType<typeof vi.fn<(req: InviteMemberRequest) => void>>
  updateRole: ReturnType<typeof vi.fn<(req: UpdateMemberRoleRequest) => void>>
  remove: ReturnType<typeof vi.fn<(req: RemoveMemberRequest) => void>>
}

const alice = {
  userId: 'u-1',
  email: 'alice@example.com',
  displayName: 'Alice Adams',
  role: MemberRole.OWNER,
  state: MemberState.ACTIVE,
  lastLogin: '',
}
const bob = {
  userId: 'u-2',
  email: 'bob@example.com',
  displayName: 'Bob Brown',
  role: MemberRole.MEMBER,
  state: MemberState.INITIAL,
  lastLogin: '',
}

function buildTransport(): { spies: Spies } {
  const spies: Spies = {
    list: vi.fn(),
    invite: vi.fn(),
    updateRole: vi.fn(),
    remove: vi.fn(),
  }
  const transport = createRouterTransport(({ service }) => {
    service(AdminService, {
      getTenantSettings: () =>
        create(GetTenantSettingsResponseSchema, {
          settings: create(TenantSettingsSchema, {
            name: 'Acme',
            publicId: 'tnt_t',
            invitedTeamAt: '',
            configuredAt: '',
          }),
          zitadelOrgId: 'org-123',
        }),
      listMembers: (req: ListMembersRequest) => {
        spies.list(req)
        const all = [create(MemberSchema, alice), create(MemberSchema, bob)]
        const filtered = all.filter((m) => {
          if (req.roleFilter !== MemberRole.UNSPECIFIED && m.role !== req.roleFilter) return false
          if (req.search) {
            const q = req.search.toLowerCase()
            return m.displayName.toLowerCase().includes(q) || m.email.toLowerCase().includes(q)
          }
          return true
        })
        return create(ListMembersResponseSchema, { members: filtered })
      },
      inviteMember: (req: InviteMemberRequest) => {
        spies.invite(req)
        return create(InviteMemberResponseSchema, {
          member: create(MemberSchema, {
            userId: 'u-new',
            email: req.email,
            displayName: `${req.givenName} ${req.familyName}`.trim() || req.email,
            role: req.role,
            state: MemberState.INITIAL,
          }),
        })
      },
      updateMemberRole: (req: UpdateMemberRoleRequest) => {
        spies.updateRole(req)
        return create(UpdateMemberRoleResponseSchema, {
          member: create(MemberSchema, { userId: req.userId, role: req.role }),
        })
      },
      removeMember: (req: RemoveMemberRequest) => {
        spies.remove(req)
        return create(RemoveMemberResponseSchema, {})
      },
    })
  })
  setAdminTransport(transport)
  return { spies }
}

function stubDiscovery(issuer = 'https://id.example.com') {
  vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
    const url =
      typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url
    if (url.includes('/auth/discovery')) {
      return Promise.resolve(
        new Response(JSON.stringify({ zitadelIssuer: issuer }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        }),
      )
    }
    return Promise.reject(new Error(`unexpected fetch in test: ${url}`))
  })
}

describe('Members', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    resetAdminTransport()
    resetDiscoveryCache()
    stubDiscovery()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders a row per member returned by ListMembers', async () => {
    buildTransport()
    const w = mount(Members)
    await flushPromises()

    expect(w.find('[data-testid="members-row-u-1"]').exists()).toBe(true)
    expect(w.find('[data-testid="members-row-u-2"]').exists()).toBe(true)
    expect(w.text()).toContain('Alice Adams')
    expect(w.text()).toContain('alice@example.com')
    expect(w.text()).toContain('Owner')
    expect(w.text()).toContain('Invited')
  })

  it('debounced search forwards the query to listMembers', async () => {
    const { spies } = buildTransport()
    vi.useFakeTimers()
    const w = mount(Members)
    await flushPromises()
    spies.list.mockClear()

    await w.find('[data-testid="members-search"]').setValue('bob')
    vi.advanceTimersByTime(250)
    await flushPromises()
    vi.useRealTimers()

    expect(spies.list).toHaveBeenCalledTimes(1)
    expect(spies.list.mock.calls[0][0].search).toBe('bob')
  })

  it('role filter forwards the enum to listMembers and filters the table', async () => {
    const { spies } = buildTransport()
    const w = mount(Members)
    await flushPromises()
    spies.list.mockClear()

    await w.find('[data-testid="members-role-filter"]').setValue(MemberRole.OWNER)
    await flushPromises()

    expect(spies.list).toHaveBeenCalledTimes(1)
    expect(spies.list.mock.calls[0][0].roleFilter).toBe(MemberRole.OWNER)
    expect(w.find('[data-testid="members-row-u-1"]').exists()).toBe(true)
    expect(w.find('[data-testid="members-row-u-2"]').exists()).toBe(false)
  })

  it('invite opens a modal and dispatches inviteMember', async () => {
    const { spies } = buildTransport()
    const w = mount(Members)
    await flushPromises()

    await w.find('[data-testid="members-invite-button"]').trigger('click')
    expect(document.querySelector('[data-testid="members-invite-modal"]')).not.toBeNull()

    const emailEl = document.querySelector(
      '[data-testid="members-invite-email"]',
    ) as HTMLInputElement
    emailEl.value = 'carol@example.com'
    emailEl.dispatchEvent(new Event('input'))

    const roleEl = document.querySelector(
      '[data-testid="members-invite-role"]',
    ) as HTMLSelectElement
    roleEl.value = String(MemberRole.ADMIN)
    roleEl.dispatchEvent(new Event('change'))

    const submit = document.querySelector(
      '[data-testid="members-invite-submit"]',
    ) as HTMLButtonElement
    submit.click()
    await flushPromises()

    expect(spies.invite).toHaveBeenCalledTimes(1)
    expect(spies.invite.mock.calls[0][0].email).toBe('carol@example.com')
    expect(spies.invite.mock.calls[0][0].role).toBe(MemberRole.ADMIN)
  })

  it('edit dispatches updateMemberRole with the chosen role', async () => {
    const { spies } = buildTransport()
    const w = mount(Members)
    await flushPromises()

    await w.find('[data-testid="members-edit-u-2"]').trigger('click')
    const roleEl = document.querySelector('[data-testid="members-edit-role"]') as HTMLSelectElement
    roleEl.value = String(MemberRole.ADMIN)
    roleEl.dispatchEvent(new Event('change'))

    const submit = document.querySelector(
      '[data-testid="members-edit-submit"]',
    ) as HTMLButtonElement
    submit.click()
    await flushPromises()

    expect(spies.updateRole).toHaveBeenCalledTimes(1)
    expect(spies.updateRole.mock.calls[0][0]).toMatchObject({
      userId: 'u-2',
      role: MemberRole.ADMIN,
    })
  })

  it('remove asks for confirmation and dispatches removeMember', async () => {
    const { spies } = buildTransport()
    const w = mount(Members)
    await flushPromises()

    await w.find('[data-testid="members-remove-u-2"]').trigger('click')
    await flushPromises()

    const dialog = document.querySelector('[role="dialog"]') as HTMLElement | null
    expect(dialog).not.toBeNull()
    const primary = dialog!.querySelector('button') as HTMLButtonElement
    // ErrorModal renders primary button first; click it.
    const buttons = Array.from(dialog!.querySelectorAll('button')) as HTMLButtonElement[]
    const removeBtn = buttons.find((b) => b.textContent?.includes('Remove'))!
    removeBtn.click()
    await flushPromises()
    expect(primary).toBeTruthy()
    expect(spies.remove).toHaveBeenCalledTimes(1)
    expect(spies.remove.mock.calls[0][0].userId).toBe('u-2')
  })

  it('renders the identity & policies disclosure with deep-link cards', async () => {
    buildTransport()
    const w = mount(Members)
    await flushPromises()

    const anchors = w.findAll('[data-testid^="zitadel-directory-"]')
    expect(anchors.length).toBeGreaterThanOrEqual(4)
    for (const a of anchors) {
      const href = a.attributes('href') ?? ''
      expect(href.startsWith('https://id.example.com')).toBe(true)
      expect(href).toContain('org=org-123')
    }
    // The role-assignment deep-link has been removed; Limen now manages roles in-app.
    expect(w.find('[data-testid="members-role-assignment-link"]').exists()).toBe(false)
  })
})
