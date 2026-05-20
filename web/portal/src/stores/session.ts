import { defineStore } from 'pinia'
import { ref } from 'vue'
import { portalClient } from '../api/client'

export interface PortalUser {
  subject: string
  email: string
  name: string
  roles: string[]
}

// useSessionStore tracks the portal-cookie session. The SPA never
// touches the cookie directly; GetSession is the source of truth.
// loaded distinguishes "we haven't asked yet" from "we asked and the
// user isn't authenticated" — the router guard depends on this to
// avoid an infinite redirect loop on the /login page itself.
export const useSessionStore = defineStore('session', () => {
  const loaded = ref(false)
  const authenticated = ref(false)
  const user = ref<PortalUser | null>(null)
  const loginUrl = ref<string>('')

  async function refresh(): Promise<void> {
    try {
      const resp = await portalClient().getSession({})
      authenticated.value = resp.authenticated
      loginUrl.value = resp.loginUrl
      if (resp.authenticated && resp.user) {
        user.value = {
          subject: resp.user.subject,
          email: resp.user.email,
          name: resp.user.name,
          roles: [...resp.roles],
        }
      } else {
        user.value = null
      }
    } catch (err) {
      // GetSession is the only RPC that should never fail under normal
      // operation; surface the error to the console and treat as
      // logged-out so the login page can render.
      console.error('GetSession failed', err)
      authenticated.value = false
      user.value = null
    } finally {
      loaded.value = true
    }
  }

  function logout(): void {
    // Triggers the Phase-4 /auth/logout handler, which clears the
    // portal cookie and bounces to Zitadel's end-session endpoint.
    const match = window.location.pathname.match(/^(\/t\/[^/]+)\//)
    const prefix = match ? match[1] : ''
    window.location.href = `${prefix}/auth/logout`
  }

  return { loaded, authenticated, user, loginUrl, refresh, logout }
})
