import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  adminClient,
  isAdminAuthError,
  type AdminAuthErrorKind,
  type Role,
} from '@/transport/adminClient'

export interface SessionUser {
  firstName: string
  email: string
}

export interface SessionTenant {
  publicId: string
  name: string
}

// useSessionStore caches the result of AdminService.GetSession.
//
// - `loaded`  : "we've asked the server at least once". The router
//               guard waits on this before letting any non-public
//               route render.
// - `error`   : non-null when GetSession returned an auth-shaped
//               failure. The guard inspects this to decide between
//               redirecting to /auth/login (unauthenticated) and
//               /forbidden (permission_denied).
// - `bootstrap()` is the idempotent entry point used by the guard;
//   `refresh()` always re-fetches and is used after explicit user
//   actions (logout, invite team, etc.).
export const useSessionStore = defineStore('session', () => {
  const loaded = ref(false)
  const tenant = ref<SessionTenant | null>(null)
  const user = ref<SessionUser | null>(null)
  const role = ref<Role | null>(null)
  const error = ref<AdminAuthErrorKind | null>(null)
  let inFlight: Promise<void> | null = null

  async function refresh(): Promise<void> {
    try {
      const resp = await adminClient().getSession()
      tenant.value = resp.tenant
      user.value = resp.user
      role.value = resp.role
      error.value = null
    } catch (err) {
      tenant.value = null
      user.value = null
      role.value = null
      if (isAdminAuthError(err)) {
        error.value = err.kind
      } else {
        console.error('AdminService.GetSession failed', err)
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
