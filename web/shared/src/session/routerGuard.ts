import type { Router } from 'vue-router'

import { useSessionStore } from './store.ts'

export interface SessionGuardOptions {
  // loginUrl builds the absolute backend login URL for a given
  // SPA-relative `returnTo` path. Each SPA owns its own tenant-prefix
  // discovery; the guard only knows the SPA-relative path the user
  // was trying to reach.
  loginUrl: (returnTo: string) => string
  // forbiddenRouteName is the in-SPA route the guard navigates to
  // when GetSession returns `permission_denied`. SPAs that don't
  // ship a /forbidden page should omit this and the guard will
  // refuse navigation instead (returning `false`).
  forbiddenRouteName?: string
  // publicRouteFlag is the route.meta key that opts a route out of
  // the session check. Defaults to "public".
  publicRouteFlag?: string
}

// createSessionGuard installs a single beforeEach hook on the given
// router. The shape is:
//
//   1. Routes flagged public (route.meta[publicRouteFlag] === true)
//      render without a session check.
//   2. bootstrap() runs at most once; concurrent callers share the
//      same promise.
//   3. On `unauthenticated`, hard-redirect to options.loginUrl(path).
//   4. On `permission_denied`, route to forbiddenRouteName (or refuse
//      if not provided).
//
// Both SPAs install this — no per-SPA guard code remains.
export function createSessionGuard(router: Router, options: SessionGuardOptions): void {
  const publicFlag = options.publicRouteFlag ?? 'public'
  router.beforeEach(async (to) => {
    if (to.meta[publicFlag] === true) return true
    const session = useSessionStore()
    await session.bootstrap()
    if (session.error === 'unauthenticated') {
      window.location.replace(options.loginUrl(to.fullPath))
      return false
    }
    if (session.error === 'permission_denied') {
      if (options.forbiddenRouteName) {
        return { name: options.forbiddenRouteName }
      }
      return false
    }
    return true
  })
}
