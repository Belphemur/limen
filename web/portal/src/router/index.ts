import { createRouter as createVueRouter, createWebHistory, type Router } from 'vue-router'

import { createSessionGuard, discoverSpaBasePath, tenantLoginUrl } from '@limen/shared/session'

import SignedOut from '../pages/SignedOut.vue'
import Dashboard from '../pages/Dashboard.vue'
import Upstreams from '../pages/Upstreams.vue'
import MCPClients from '../pages/MCPClients.vue'
import Settings from '../pages/Settings.vue'

export function createRouter(): Router {
  const router = createVueRouter({
    history: createWebHistory(discoverSpaBasePath('portal')),
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
