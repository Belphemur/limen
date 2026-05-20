import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory } from 'vue-router'
import {
  setAdminClient,
  resetAdminClient,
  AdminAuthError,
  type AdminClient,
} from '@/transport/adminClient'
import { createRouter } from '@/router'
import { useSessionStore } from '@/stores/session'

function clientThrowing(kind: 'unauthenticated' | 'permission_denied'): AdminClient {
  return {
    getSession: () => Promise.reject(new AdminAuthError(kind)),
    listUpstreams: () => Promise.reject(new AdminAuthError(kind)),
    getTenantSettings: () => Promise.reject(new AdminAuthError(kind)),
    markInvitedTeam: () => Promise.reject(new AdminAuthError(kind)),
  }
}

function clientHappy(): AdminClient {
  return {
    getSession: () =>
      Promise.resolve({
        tenant: { publicId: 'tnt_t', name: 'Acme' },
        user: { firstName: 'Alex', email: 'alex@acme.example' },
        role: 'owner',
      }),
    listUpstreams: () => Promise.resolve({ upstreams: [] }),
    getTenantSettings: () =>
      Promise.resolve({ name: 'Acme', invitedTeamAt: null, configuredAt: null }),
    markInvitedTeam: () => Promise.resolve(),
  }
}

describe('router guard', () => {
  let replaceSpy: ReturnType<typeof vi.fn>
  let originalLocation: Location

  beforeEach(() => {
    setActivePinia(createPinia())
    resetAdminClient()
    replaceSpy = vi.fn()
    originalLocation = window.location
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        pathname: '/t/acme/admin/',
        search: '',
        href: 'http://localhost/t/acme/admin/',
        replace: replaceSpy,
      },
    })
  })

  afterEach(() => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: originalLocation,
    })
    resetAdminClient()
  })

  it('redirects to tenant /auth/login on unauthenticated', async () => {
    setAdminClient(clientThrowing('unauthenticated'))
    const router = createRouter({ history: createMemoryHistory() })
    await router.push('/mcp-servers')
    expect(replaceSpy).toHaveBeenCalledTimes(1)
    expect(replaceSpy.mock.calls[0][0]).toBe(
      '/t/acme/auth/login?return_to=' + encodeURIComponent('/mcp-servers'),
    )
  })

  it('routes to /forbidden on permission_denied', async () => {
    setAdminClient(clientThrowing('permission_denied'))
    const router = createRouter({ history: createMemoryHistory() })
    await router.push('/mcp-servers')
    expect(replaceSpy).not.toHaveBeenCalled()
    expect(router.currentRoute.value.name).toBe('forbidden')
  })

  it('lets authenticated traffic through and populates the session store', async () => {
    setAdminClient(clientHappy())
    const router = createRouter({ history: createMemoryHistory() })
    await router.push('/mcp-servers')
    const session = useSessionStore()
    expect(session.loaded).toBe(true)
    expect(session.error).toBeNull()
    expect(session.user?.firstName).toBe('Alex')
    expect(router.currentRoute.value.path).toBe('/mcp-servers')
  })

  it('skips bootstrap for public routes (/forbidden)', async () => {
    const spy = vi.fn(() => Promise.reject(new AdminAuthError('unauthenticated')))
    setAdminClient({
      getSession: spy,
      listUpstreams: () => Promise.resolve({ upstreams: [] }),
      getTenantSettings: () =>
        Promise.resolve({ name: '', invitedTeamAt: null, configuredAt: null }),
      markInvitedTeam: () => Promise.resolve(),
    })
    const router = createRouter({ history: createMemoryHistory() })
    await router.push('/forbidden')
    expect(spy).not.toHaveBeenCalled()
    expect(replaceSpy).not.toHaveBeenCalled()
  })
})
