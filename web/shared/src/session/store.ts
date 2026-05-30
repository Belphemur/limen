import { defineStore } from 'pinia'
import { ref } from 'vue'
import { Code, ConnectError } from '@connectrpc/connect'

import { Role } from '@shared-gen/limen/session/v1/session_pb.ts'
import { createSessionClient } from './sessionClient.ts'
import {
  SessionAuthError,
  isSessionAuthError,
  type SessionAuthErrorKind,
} from './authError.ts'

// SessionRole is the wire-side enum re-exposed as a string union for
// SPAs that don't want to import the protobuf enum directly. The
// store always emits the string form; consumers compare against
// 'owner' / 'admin' / 'member' / 'unspecified'.
export type SessionRole = 'unspecified' | 'member' | 'admin' | 'owner'

export interface SessionUser {
  id: string
  email: string
  firstName: string
  lastName: string
}

export interface SessionTenant {
  publicId: string
  name: string
}


function roleToString(r: Role): SessionRole {
  switch (r) {
    case Role.OWNER:
      return 'owner'
    case Role.ADMIN:
      return 'admin'
    case Role.MEMBER:
      return 'member'
    default:
      return 'unspecified'
  }
}

function mapConnectError(err: unknown): SessionAuthError | null {
  if (isSessionAuthError(err)) return err
  if (err instanceof ConnectError) {
    if (err.code === Code.Unauthenticated) return new SessionAuthError('unauthenticated')
    if (err.code === Code.PermissionDenied) return new SessionAuthError('permission_denied')
  }
  return null
}

// useSessionStore is the single source of truth for "who is the
// current user, on which tenant?" across every SPA. The store is
// intentionally minimal — the shape is locked here so new SPAs
// cannot redefine it.
//
//   - loaded     : the store has completed at least one bootstrap. The
//                  router guard waits on this before letting any
//                  non-public route render.
//   - error      : set when bootstrap/refresh ended on an auth-shaped
//                  Connect status. The router guard reads this to
//                  decide between hard-redirect (unauthenticated) and
//                  in-SPA route (permission_denied).
//   - bootstrap(): idempotent, dedupes concurrent callers via
//                  inFlight. Used by the router guard.
//   - refresh()  : always re-fetches. Used after explicit user
//                  actions (logout, invite team, etc.).
//   - logout()   : hard-redirects to ${tenantPrefix}/auth/logout. The
//                  tenant prefix is derived from the live pathname.
export const useSessionStore = defineStore('session', () => {
  const loaded = ref(false)
  const tenant = ref<SessionTenant | null>(null)
  const user = ref<SessionUser | null>(null)
  const role = ref<SessionRole>('unspecified')
  const error = ref<SessionAuthErrorKind | null>(null)
  let inFlight: Promise<void> | null = null

  async function refresh(): Promise<void> {
    try {
      const resp = await createSessionClient().getSession({})
      tenant.value = resp.tenant
        ? { publicId: resp.tenant.publicId, name: resp.tenant.name }
        : null
      user.value = resp.user
        ? {
            id: resp.user.id,
            email: resp.user.email,
            firstName: resp.user.firstName,
            lastName: resp.user.lastName,
          }
        : null
      role.value = roleToString(resp.role)
      error.value = null
    } catch (err) {
      tenant.value = null
      user.value = null
      role.value = 'unspecified'
      const mapped = mapConnectError(err)
      if (mapped) {
        error.value = mapped.kind
      } else {
        console.error('SessionService.GetSession failed', err)
        error.value = 'unauthenticated'
      }
    } finally {
      loaded.value = true
    }
  }

  async function bootstrap(): Promise<void> {
    if (loaded.value) return
    if (!inFlight) {
      inFlight = refresh().finally(() => {
        inFlight = null
      })
    }
    await inFlight
  }

  function logout(): void {
    const match = window.location.pathname.match(/^(\/t\/[^/]+)\//)
    const prefix = match ? match[1] : ''
    window.location.href = `${prefix}/auth/logout`
  }

  return { loaded, tenant, user, role, error, refresh, bootstrap, logout }
})
