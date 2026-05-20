import { createRouter as createVueRouter, createWebHistory, type Router } from 'vue-router'

import { createSessionGuard } from '@limen/shared/session'

import SignedOut from '../pages/SignedOut.vue'
import Dashboard from '../pages/Dashboard.vue'
import Upstreams from '../pages/Upstreams.vue'
import MCPClients from '../pages/MCPClients.vue'
import Settings from '../pages/Settings.vue'

// Discover the portal's base path at runtime so a single Vite build
// serves every tenant. Two layouts are accepted:
//
//   /t/<tenant>/portal/<spa-route>   (production — Limen file_server)
//   /t/<tenant>/<spa-route>          (vite dev — bare tenant prefix)
function discoverBasePath(): string {
  const match = window.location.pathname.match(/^(\/t\/[^/]+\/(?:portal\/)?)/)
  return match ? match[1] : '/'
}

// Build the backend login URL for the current tenant. The /auth/login
// endpoint is served by Phase 4 next to every tenant-scoped SPA mount,
// so we just lift the /t/<tenant>/ prefix off the current pathname.
function tenantLoginUrl(returnTo: string): string {
  const match = window.location.pathname.match(/^(\/t\/[^/]+)\//)
  const prefix = match ? match[1] : ''
  return `${prefix}/auth/login?return_to=${encodeURIComponent(returnTo)}`
}

export function createRouter(): Router {
  const router = createVueRouter({
    history: createWebHistory(discoverBasePath()),
    routes: [
      { path: '/signed-out', name: 'signed-out', component: SignedOut, meta: { public: true } },
      { path: '/', name: 'dashboard', component: Dashboard },
      { path: '/mcp-servers', name: 'mcp-servers', component: Upstreams },
      { path: '/mcp-clients', name: 'mcp-clients', component: MCPClients },
      { path: '/settings', name: 'settings', component: Settings },
    ],
  })

  createSessionGuard(router, { loginUrl: tenantLoginUrl })

  return router
}
