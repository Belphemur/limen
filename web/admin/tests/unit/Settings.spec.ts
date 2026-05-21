import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createRouterTransport } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { resetDiscoveryCache } from '@limen/shared'
import {
  AdminService,
  GetTenantSettingsResponseSchema,
  TenantSettingsSchema,
  UpdateTenantSettingsResponseSchema,
  DeleteTenantResponseSchema,
  type GetTenantSettingsResponse,
  type UpdateTenantSettingsRequest,
  type UpdateTenantSettingsResponse,
  type DeleteTenantResponse,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import Settings from '@/pages/Settings.vue'
import { setAdminTransport, resetAdminTransport } from '@/transport/adminClient'

interface AdminStub {
  getTenantSettings?: () => GetTenantSettingsResponse
  updateTenantSettings?: (req: UpdateTenantSettingsRequest) => UpdateTenantSettingsResponse
  deleteTenant?: () => DeleteTenantResponse
}

function buildTransport(stub: AdminStub) {
  return createRouterTransport(({ service }) => {
    service(AdminService, {
      getTenantSettings:
        stub.getTenantSettings ??
        (() =>
          create(GetTenantSettingsResponseSchema, {
            settings: create(TenantSettingsSchema, {
              name: 'Acme',
              publicId: 'tnt_t',
              invitedTeamAt: '',
              configuredAt: '',
            }),
            dcrRedirectUriAllowlist: ['https://app.acme.com/cb'],
            zitadelOrgId: 'z-org',
          })),
      updateTenantSettings:
        stub.updateTenantSettings ??
        ((req) =>
          create(UpdateTenantSettingsResponseSchema, {
            settings: create(TenantSettingsSchema, {
              name: req.name.trim() || 'Acme',
              publicId: 'tnt_t',
              invitedTeamAt: '',
              configuredAt: '',
            }),
            dcrRedirectUriAllowlist: req.dcrRedirectUriAllowlistSet
              ? req.dcrRedirectUriAllowlist
              : ['https://app.acme.com/cb'],
          })),
      deleteTenant: stub.deleteTenant ?? (() => create(DeleteTenantResponseSchema, {})),
    })
  })
}

function stubDiscovery(issuer = 'https://idp.example') {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url =
      typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url
    if (url.includes('/auth/discovery')) {
      return new Response(JSON.stringify({ zitadelIssuer: issuer }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch in test: ${url}`)
  })
}

async function mountPage(stub: AdminStub = {}) {
  setAdminTransport(buildTransport(stub))
  const wrapper = mount(Settings)
  await flushPromises()
  return wrapper
}

describe('Settings', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    resetAdminTransport()
    resetDiscoveryCache()
    stubDiscovery()
    // jsdom does not implement <dialog>; stub the modal methods.
    if (!('showModal' in HTMLDialogElement.prototype)) {
      // @ts-expect-error jsdom shim
      HTMLDialogElement.prototype.showModal = function () {
        this.open = true
      }
      // @ts-expect-error jsdom shim
      HTMLDialogElement.prototype.close = function () {
        this.open = false
        this.dispatchEvent(new Event('close'))
      }
    }
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('hydrates the four sections from GetTenantSettings + /auth/discovery', async () => {
    const w = await mountPage()
    expect(w.find('[data-testid="settings-loading"]').exists()).toBe(false)
    expect(w.find('[data-testid="section-organization"]').exists()).toBe(true)
    expect(w.find('[data-testid="section-zitadel"]').exists()).toBe(true)
    expect(w.find('[data-testid="section-allowlist"]').exists()).toBe(true)
    expect(w.find('[data-testid="section-danger"]').exists()).toBe(true)

    expect((w.find('[data-testid="org-name-input"]').element as HTMLInputElement).value).toBe(
      'Acme',
    )
    expect(w.find('[data-testid="org-public-id"]').text()).toBe('tnt_t')
    expect(w.find('[data-testid="zitadel-org-id"]').text()).toBe('z-org')

    const link = w.find('[data-testid="zitadel-console-link"]')
    expect(link.exists()).toBe(true)
    expect(link.attributes('href')).toBe('https://idp.example/ui/console/users?org=z-org')
  })

  it('keeps Save name disabled until the value diverges from the loaded name', async () => {
    const w = await mountPage()
    const save = w.find('[data-testid="org-save"]')
    expect((save.element as HTMLButtonElement).disabled).toBe(true)
    await w.find('[data-testid="org-name-input"]').setValue('New Co')
    expect((save.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('saves the allowlist via UpdateTenantSettings with the sentinel flag set', async () => {
    const calls: UpdateTenantSettingsRequest[] = []
    const w = await mountPage({
      updateTenantSettings: (req) => {
        calls.push(req)
        return create(UpdateTenantSettingsResponseSchema, {
          settings: create(TenantSettingsSchema, {
            name: 'Acme',
            publicId: 'tnt_t',
            invitedTeamAt: '',
            configuredAt: '',
          }),
          dcrRedirectUriAllowlist: ['https://app.acme.com/cb', 'https://*.acme.com/cb'],
        })
      },
    })
    await w.find('[data-testid="allowlist-add"]').trigger('click')
    await flushPromises()
    const inputs = w.findAll('[data-testid="section-allowlist"] input[type=text]')
    await inputs[1].setValue('https://*.acme.com/cb')
    await flushPromises()
    await w.find('[data-testid="allowlist-save"]').trigger('click')
    await flushPromises()

    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({
      dcrRedirectUriAllowlistSet: true,
      dcrRedirectUriAllowlist: ['https://app.acme.com/cb', 'https://*.acme.com/cb'],
    })
  })

  it('disables Save allowlist when a row is invalid', async () => {
    const w = await mountPage()
    await w.find('[data-testid="allowlist-add"]').trigger('click')
    await flushPromises()
    const inputs = w.findAll('[data-testid="section-allowlist"] input[type=text]')
    await inputs[1].setValue('no-scheme')
    await flushPromises()
    const save = w.find('[data-testid="allowlist-save"]').element as HTMLButtonElement
    expect(save.disabled).toBe(true)
  })

  it('keeps the delete confirm button disabled until the public ID is typed exactly', async () => {
    const w = await mountPage()
    await w.find('[data-testid="danger-open"]').trigger('click')
    await flushPromises()
    const confirm = w.find('[data-testid="danger-confirm"]').element as HTMLButtonElement
    expect(confirm.disabled).toBe(true)
    await w.find('[data-testid="danger-confirm-input"]').setValue('wrong')
    expect(confirm.disabled).toBe(true)
    await w.find('[data-testid="danger-confirm-input"]').setValue('tnt_t')
    await flushPromises()
    expect(confirm.disabled).toBe(false)
  })

  it('redirects to /forbidden on successful delete', async () => {
    const assign = vi.fn()
    Object.defineProperty(window, 'location', {
      writable: true,
      value: { assign, href: 'http://test/' },
    })
    const w = await mountPage()
    await w.find('[data-testid="danger-open"]').trigger('click')
    await flushPromises()
    await w.find('[data-testid="danger-confirm-input"]').setValue('tnt_t')
    await flushPromises()
    await w.find('[data-testid="danger-confirm"]').trigger('click')
    await flushPromises()
    expect(assign).toHaveBeenCalledWith('/forbidden')
  })
})
