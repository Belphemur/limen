import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory } from 'vue-router'
import { Code, ConnectError, createRouterTransport, type Transport } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { setSessionTransport, resetSessionTransport, useSessionStore } from '@limen/shared/session'
import {
  SessionService,
  GetSessionResponseSchema,
  Role,
} from '@limen/shared/gen/limen/session/v1/session_pb.ts'
import { createRouter } from '@/router'

function unauthenticatedTransport(): Transport {
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => {
        throw new ConnectError('unauthenticated', Code.Unauthenticated)
      },
    })
  })
}

function permissionDeniedTransport(): Transport {
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => {
        throw new ConnectError('forbidden', Code.PermissionDenied)
      },
    })
  })
}

function happyTransport(spy?: () => void): Transport {
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => {
        if (spy) spy()
        return create(GetSessionResponseSchema, {
          tenant: { publicId: 'tnt_t', name: 'Acme' },
          user: {
            id: 'usr_1',
            email: 'alex@acme.example',
            firstName: 'Alex',
            lastName: 'Doe',
          },
          role: Role.OWNER,
        })
      },
    })
  })
}

describe('router guard', () => {
  let replaceSpy: ReturnType<typeof vi.fn>
  let originalLocation: Location

  beforeEach(() => {
    setActivePinia(createPinia())
    resetSessionTransport()
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
    resetSessionTransport()
  })

  it('redirects to tenant /auth/login on unauthenticated', async () => {
    setSessionTransport(unauthenticatedTransport())
    const router = createRouter({ history: createMemoryHistory() })
    await router.push('/mcp-servers')
    expect(replaceSpy).toHaveBeenCalledTimes(1)
    expect(replaceSpy.mock.calls[0][0]).toBe(
      '/t/acme/auth/login?return_to=' + encodeURIComponent('/mcp-servers'),
    )
  })

  it('routes to /forbidden on permission_denied', async () => {
    setSessionTransport(permissionDeniedTransport())
    const router = createRouter({ history: createMemoryHistory() })
    await router.push('/mcp-servers')
    expect(replaceSpy).not.toHaveBeenCalled()
    expect(router.currentRoute.value.name).toBe('forbidden')
  })

  it('lets authenticated traffic through and populates the session store', async () => {
    setSessionTransport(happyTransport())
    const router = createRouter({ history: createMemoryHistory() })
    await router.push('/mcp-servers')
    const session = useSessionStore()
    expect(session.loaded).toBe(true)
    expect(session.error).toBeNull()
    expect(session.user?.firstName).toBe('Alex')
    expect(session.role).toBe('owner')
    expect(router.currentRoute.value.path).toBe('/mcp-servers')
  })

  it('skips bootstrap for public routes (/forbidden)', async () => {
    const spy = vi.fn()
    setSessionTransport(happyTransport(spy))
    const router = createRouter({ history: createMemoryHistory() })
    await router.push('/forbidden')
    expect(spy).not.toHaveBeenCalled()
    expect(replaceSpy).not.toHaveBeenCalled()
  })
})
