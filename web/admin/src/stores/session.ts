import { defineStore } from 'pinia'
import { ref } from 'vue'
import { adminClient, type Role } from '@/transport/adminClient'

export interface SessionUser {
  firstName: string
  email: string
}

export interface SessionTenant {
  publicId: string
  name: string
}

export const useSessionStore = defineStore('session', () => {
  const loaded = ref(false)
  const tenant = ref<SessionTenant | null>(null)
  const user = ref<SessionUser | null>(null)
  const role = ref<Role | null>(null)

  async function refresh(): Promise<void> {
    try {
      const resp = await adminClient().getSession()
      tenant.value = resp.tenant
      user.value = resp.user
      role.value = resp.role
    } catch (err) {
      console.error('AdminService.GetSession failed', err)
      tenant.value = null
      user.value = null
      role.value = null
    } finally {
      loaded.value = true
    }
  }

  function logout(): void {
    const match = window.location.pathname.match(/^(\/t\/[^/]+)\//)
    const prefix = match ? match[1] : ''
    window.location.href = `${prefix}/auth/logout`
  }

  return { loaded, tenant, user, role, refresh, logout }
})
