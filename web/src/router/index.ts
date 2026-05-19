import { createRouter as createVueRouter, createWebHistory, type Router } from 'vue-router'

import Login from '../pages/Login.vue'
import Dashboard from '../pages/Dashboard.vue'
import Upstreams from '../pages/Upstreams.vue'
import MCPClients from '../pages/MCPClients.vue'
import Settings from '../pages/Settings.vue'

import { useSessionStore } from '../stores/session'

// Discover the portal's base path at runtime so a single Vite build
// serves every tenant. The URL shape Limen mounts is:
//
//   /t/<tenant>/portal/<spa-route>
//
// We grab everything up to and including `/portal/` and feed it to
// createWebHistory as the SPA's base. If the prefix is missing (dev
// server hits `/` directly), fall back to "/" so local development
// still works.
function discoverBasePath(): string {
  const match = window.location.pathname.match(/^(\/t\/[^/]+\/portal\/)/)
  return match ? match[1] : '/'
}

export function createRouter(): Router {
  const router = createVueRouter({
    history: createWebHistory(discoverBasePath()),
    routes: [
      { path: '/login', name: 'login', component: Login, meta: { public: true } },
      { path: '/', name: 'dashboard', component: Dashboard },
      { path: '/upstreams', name: 'upstreams', component: Upstreams },
      { path: '/mcp-clients', name: 'mcp-clients', component: MCPClients },
      { path: '/settings', name: 'settings', component: Settings },
    ],
  })

  router.beforeEach(async (to) => {
    if (to.meta.public) return true
    const session = useSessionStore()
    if (!session.loaded) {
      await session.refresh()
    }
    if (!session.authenticated) {
      return { name: 'login', query: { return_to: to.fullPath } }
    }
    return true
  })

  return router
}
