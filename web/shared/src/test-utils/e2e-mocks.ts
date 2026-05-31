import type { BrowserContext, Page } from '@playwright/test'

/**
 * Default tenant slug used by all e2e tests.
 * Must match the __LIMEN_TENANT__ injected via context.addInitScript.
 */
export const TENANT = 'acme'

/**
 * Protobuf-JSON format for a valid admin session.
 * Uses string-valued enums (ROLE_ADMIN, not numeric 3) because
 * @bufbuild/protobuf v2 deserializes enums by name.
 */
export const ADMIN_SESSION = {
  tenant: { publicId: 'tnt_acme', name: 'Acme Corp' },
  user: { email: 'alex@acme.example', firstName: 'Alex' },
  role: 'ROLE_ADMIN',
} as const

/**
 * Injects window.__LIMEN_TENANT__ into the browser context so the
 * SPA's discoverTenant() function finds the right slug without
 * needing a real /t/<tenant>/ URL path.
 */
export function injectTenant(context: BrowserContext, tenant = TENANT): Promise<{ dispose: () => Promise<void> }> {
  return context.addInitScript((slug) => {
    ;(window as Window & { __LIMEN_TENANT__?: string }).__LIMEN_TENANT__ = slug
  }, tenant)
}

/**
 * Overrides window.fetch to intercept SessionService.GetSession
 * calls at the JavaScript level. This is more reliable than
 * Playwright's page.route() with regex patterns for Connect-RPC
 * POST requests because route matching can be flaky with ports
 * and base URLs.
 *
 * @param context     — Playwright BrowserContext
 * @param session     — The protobuf-JSON session object to return
 *                       (defaults to ADMIN_SESSION)
 *
 * Why fetch override instead of page.route:
 *   page.route with regex patterns was unreliable for intercepting
 *   Connect-RPC POST requests (the regex /\/t\/acme\/api\/.../
 *   would sometimes not match URLs containing ports like :4174).
 *   Overriding fetch directly at the JS level guarantees the mock
 *   fires before any network request, eliminating the race.
 */
export function mockSessionFetch(
  context: BrowserContext,
  session: Record<string, unknown> = ADMIN_SESSION as unknown as Record<string, unknown>,
): Promise<{ dispose: () => Promise<void> }> {
  return context.addInitScript((sessionJson) => {
    const session = JSON.parse(sessionJson) as Record<string, unknown>
    const origFetch = window.fetch.bind(window)
    window.fetch = (input, init) => {
      const url =
        typeof input === 'string'
          ? input
          : input instanceof URL
            ? input.href
            : 'url' in (input as Request)
              ? (input as Request).url
              : ''
      if (url.includes('/limen.session.v1.SessionService/GetSession')) {
        return Promise.resolve(
          new Response(JSON.stringify(session), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }),
        )
      }
      return origFetch(input, init)
    }
  }, JSON.stringify(session))
}

/**
 * Intercepts GET requests to /auth/login — the URL the SPA's
 * router guard hard-redirects to when GetSession fails.
 *
 * Without a real backend, vite preview has no server-side
 * /auth/login endpoint. This interceptor serves the SPA's built
 * index.html with __LIMEN_TENANT__ injected so the SPA boots
 * directly from the auth URL, calls GetSession (which is mocked
 * by mockSessionFetch), and navigates to the return_to URL.
 *
 * @param page         — Playwright Page
 * @param indexHtml    — Contents of dist/index.html (read with
 *                       readFileSync before tests run)
 *
 * How it works:
 *   1. Guard redirects to /auth/login?return_to=/mcp-servers
 *   2. This interceptor serves the SPA's index.html with
 *      __LIMEN_TENANT__ injected
 *   3. The SPA boots, mockSessionFetch returns a valid session
 *   4. After boot, setTimeout redirects to the return_to URL
 *   5. The SPA renders the intended page
 */
export async function interceptAuthLogin(
  page: Page,
  indexHtml: string,
  tenant = TENANT,
): Promise<void> {
  await page.route('**/auth/login**', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const url = new URL(route.request().url())
    const returnTo = url.searchParams.get('return_to') || '/'
    await route.fulfill({
      status: 200,
      contentType: 'text/html',
      body: indexHtml
        .replace('</head>', `<script>window.__LIMEN_TENANT__="${tenant}"</script></head>`)
        .replace(
          '</body>',
          `<script>setTimeout(function(){window.location.replace(${JSON.stringify(returnTo)})},0)</script></body>`,
        ),
    })
  })
}

/**
 * Standard Connect-RPC JSON response envelope.
 * All Limen Connect-RPC services use application/json for unary calls.
 */
export function rpc(body: unknown): { status: number; contentType: string; body: string } {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) }
}
