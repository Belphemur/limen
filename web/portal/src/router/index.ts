import { createRouter as createVueRouter, createWebHistory, type Router } from 'vue-router'

import Login from '../pages/Login.vue'
import SignedOut from '../pages/SignedOut.vue'
import Dashboard from '../pages/Dashboard.vue'
import Upstreams from '../pages/Upstreams.vue'
import MCPClients from '../pages/MCPClients.vue'
import Settings from '../pages/Settings.vue'

import { useSessionStore } from '../stores/session'

// Discover the portal's base path at runtime so a single Vite build
// serves every tenant. Two layouts are accepted:
//
//   /t/<tenant>/portal/<spa-route>   (production — Limen file_server)
//   /t/<tenant>/<spa-route>          (vite dev — bare tenant prefix)
//
// We grab everything up to and including the tenant segment (plus the
// optional /portal/) and feed it to createWebHistory as the SPA's base.
// If neither prefix is present, fall back to "/" so isolated tests and
// the bare http://localhost:5173/ entry point still work.
function discoverBasePath(): string {
  const match = window.location.pathname.match(/^(\/t\/[^/]+\/(?:portal\/)?)/)
  return match ? match[1] : '/'
}

export function createRouter(): Router {
  const router = createVueRouter({
    history: createWebHistory(discoverBasePath()),
    routes: [
      { path: '/login', name: 'login', component: Login, meta: { public: true } },
      { path: '/signed-out', name: 'signed-out', component: SignedOut, meta: { public: true } },
      { path: '/', name: 'dashboard', component: Dashboard },
      { path: '/mcp-servers', name: 'mcp-servers', component: Upstreams },
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
