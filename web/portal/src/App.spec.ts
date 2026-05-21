import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { useSessionStore, type SessionRole } from '@limen/shared/session'
import App from '@/App.vue'

function buildRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/mcp-servers', component: { template: '<div />' } },
      { path: '/mcp-clients', component: { template: '<div />' } },
      { path: '/settings', component: { template: '<div />' } },
    ],
  })
}

async function mountWithRole(role: SessionRole) {
  const router = buildRouter()
  await router.push('/')
  await router.isReady()
  const w = mount(App, {
    global: {
      plugins: [router],
      stubs: { ThemeSwitcher: true },
    },
  })
  const session = useSessionStore()
  session.user = {
    id: 'usr_1',
    email: 'alex@acme.example',
    firstName: 'Alex',
    lastName: 'Doe',
  }
  session.tenant = { publicId: 'tnt_t', name: 'Acme' }
  session.role = role
  session.loaded = true
  await flushPromises()
  // Open the user menu so the chip is in the DOM.
  await w.find('button[aria-haspopup="menu"]').trigger('click')
  await flushPromises()
  return w
}

describe('Portal App header', () => {
  let originalLocation: Location

  beforeEach(() => {
    setActivePinia(createPinia())
    originalLocation = window.location
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        ...originalLocation,
        pathname: '/t/tnt_t/',
        href: 'http://localhost/t/tnt_t/',
      },
    })
  })

  afterEach(() => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: originalLocation,
    })
    vi.restoreAllMocks()
  })

  it('shows the Admin chip linking to /t/<tenant>/admin/ for admin role', async () => {
    const w = await mountWithRole('admin')
    const chip = w.find('[data-testid="portal-admin-chip"]')
    expect(chip.exists()).toBe(true)
    expect(chip.attributes('href')).toBe('/t/tnt_t/admin/')
  })

  it('shows the Admin chip for owner role', async () => {
    const w = await mountWithRole('owner')
    expect(w.find('[data-testid="portal-admin-chip"]').exists()).toBe(true)
  })

  it('hides the Admin chip for member role', async () => {
    const w = await mountWithRole('member')
    expect(w.find('[data-testid="portal-admin-chip"]').exists()).toBe(false)
  })
})
