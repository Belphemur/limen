import {
  createRouter as createVueRouter,
  createWebHistory,
  type RouteRecordRaw,
  type Router,
  type RouterHistory,
} from 'vue-router'

import { createSessionGuard, discoverSpaBasePath, tenantLoginUrl } from '@limen/shared/session'

import AdminShell from '@/layout/AdminShell.vue'
import { ROUTES, routeDefs } from './routes'

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
    history: options.history ?? createWebHistory(discoverSpaBasePath('admin')),
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
