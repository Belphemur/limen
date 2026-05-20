import { createRouter as createVueRouter, createWebHistory, type Router } from 'vue-router'

import AdminShell from '@/layout/AdminShell.vue'
import { routeDefs } from './routes'

// Discover the admin SPA's base path at runtime. Accepts:
//   /t/<tenant>/admin/<spa-route>   (production)
//   /t/<tenant>/<spa-route>         (dev convenience)
// Fallback "/" keeps standalone vite dev and unit tests happy.
function discoverBasePath(): string {
  const match = window.location.pathname.match(/^(\/t\/[^/]+\/(?:admin\/)?)/)
  return match ? match[1] : '/'
}

export function createRouter(): Router {
  return createVueRouter({
    history: createWebHistory(discoverBasePath()),
    routes: [
      {
        path: '/',
        component: AdminShell,
        children: routeDefs.map((def) => ({
          // vue-router child paths must be relative — strip the leading slash.
          path: def.path === '/' ? '' : def.path.replace(/^\//, ''),
          name: def.name,
          component: def.component,
          meta: { search: def.search },
        })),
      },
    ],
  })
}
