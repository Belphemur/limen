import {
  createRouter as createVueRouter,
  createWebHistory,
  type RouteRecordRaw,
  type Router,
  type RouterHistory,
} from 'vue-router'

import { createSessionGuard } from '@limen/shared/session'

import AdminShell from '@/layout/AdminShell.vue'
import { ROUTES, routeDefs } from './routes'

// Discover the admin SPA's base path at runtime. Accepts:
//   /t/<tenant>/admin/<spa-route>   (production)
//   /t/<tenant>/<spa-route>         (dev convenience)
// Fallback "/" keeps standalone vite dev and unit tests happy.
function discoverBasePath(): string {
  const match = window.location.pathname.match(/^(\/t\/[^/]+\/(?:admin\/)?)/)
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

export interface CreateRouterOptions {
  // Optional history override. Production uses createWebHistory; tests
  // inject createMemoryHistory.
  history?: RouterHistory
}

export function createRouter(options: CreateRouterOptions = {}): Router {
  const shellChildren: RouteRecordRaw[] = []
  const topLevel: RouteRecordRaw[] = []

  for (const def of routeDefs) {
    const record: RouteRecordRaw = {
      path: def.outsideShell ? def.path : def.path === '/' ? '' : def.path.replace(/^\//, ''),
      name: def.name,
      component: def.component,
      meta: { search: def.search, public: def.public === true },
    }
    if (def.outsideShell) {
      topLevel.push(record)
    } else {
      shellChildren.push(record)
    }
  }

  const router = createVueRouter({
    history: options.history ?? createWebHistory(discoverBasePath()),
    routes: [
      ...topLevel,
      {
        path: '/',
        component: AdminShell,
        children: shellChildren,
      },
    ],
  })

  createSessionGuard(router, {
    loginUrl: tenantLoginUrl,
    forbiddenRouteName: 'forbidden',
  })

  return router
}

export { ROUTES }
