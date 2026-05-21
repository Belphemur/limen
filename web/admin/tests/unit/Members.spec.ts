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
  type GetTenantSettingsResponse,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import Members from '@/pages/Members.vue'
import { setAdminTransport, resetAdminTransport } from '@/transport/adminClient'

function buildTransport(opts: { fail?: boolean; orgId?: string } = {}) {
  return createRouterTransport(({ service }) => {
    service(AdminService, {
      getTenantSettings: (): GetTenantSettingsResponse => {
        if (opts.fail) throw new Error('boom')
        return create(GetTenantSettingsResponseSchema, {
          settings: create(TenantSettingsSchema, {
            name: 'Acme',
            publicId: 'tnt_t',
            invitedTeamAt: '',
            configuredAt: '',
          }),
          zitadelOrgId: opts.orgId ?? 'org-123',
        })
      },
    })
  })
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

  it('renders six Zitadel Console deep-link cards scoped to the org', async () => {
    setAdminTransport(buildTransport())
    const w = mount(Members)
    await flushPromises()

    const anchors = w.findAll('[data-testid^="zitadel-directory-"]')
    expect(anchors).toHaveLength(6)
    for (const a of anchors) {
      const href = a.attributes('href') ?? ''
      expect(href.startsWith('https://id.example.com')).toBe(true)
      expect(href).toContain('org=org-123')
      expect(a.attributes('target')).toBe('_blank')
      expect(a.attributes('rel')).toBe('noopener noreferrer')
    }
    expect(w.find('[data-testid="members-loading"]').exists()).toBe(false)
    expect(w.find('[data-testid="members-error"]').exists()).toBe(false)
  })

  it('renders the error state when getTenantSettings rejects', async () => {
    setAdminTransport(buildTransport({ fail: true }))
    const w = mount(Members)
    await flushPromises()

    expect(w.find('[data-testid="members-error"]').exists()).toBe(true)
    // The grid still renders, just without an org scope.
    const anchors = w.findAll('[data-testid^="zitadel-directory-"]')
    expect(anchors).toHaveLength(6)
  })
})
