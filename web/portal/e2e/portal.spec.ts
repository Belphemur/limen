import { test, expect, type Route } from '@playwright/test'

// Connect-RPC over Connect's JSON format returns
// `application/json` for each unary call POSTed to
// /t/<tenant>/api/<service>/<method>. Playwright's route
// interception lets us reply without a Limen process. The OIDC
// callback in real life sets the portal cookie + bounces; here we
// short-circuit by just flipping the in-test `authenticated` flag
// before navigating to a protected route.

const TENANT = 'acme'
const API_PREFIX = `/t/${TENANT}/api/limen.portal.v1.PortalService/`

interface RpcState {
  authenticated: boolean
  upstreamLinkState: 'NONE' | 'CONNECTED'
}

function rpcResponse(body: unknown): Parameters<Route['fulfill']>[0] {
  return {
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify(body),
  }
}

test.describe('portal happy path (stubbed OIDC + RPC)', () => {
  test('login screen → authenticated → connect → disconnect', async ({ page, context }) => {
    const state: RpcState = { authenticated: false, upstreamLinkState: 'NONE' }

    await context.addInitScript((tenant) => {
      ;(window as Window & { __LIMEN_TENANT__?: string }).__LIMEN_TENANT__ = tenant
    }, TENANT)

    await page.route(`**${API_PREFIX}**`, async (route) => {
      const method = route.request().url().split(API_PREFIX)[1]
      switch (method) {
        case 'GetSession':
          await route.fulfill(
            rpcResponse(
              state.authenticated
                ? {
                    authenticated: true,
                    user: { subject: 'zsub_1', email: 'op@acme.example', name: 'Op' },
                    roles: ['member'],
                  }
                : { authenticated: false, loginUrl: `/auth/login?tenant=${TENANT}&return_to=/` },
            ),
          )
          return
        case 'ListUpstreams':
          await route.fulfill(
            rpcResponse({
              upstreams: [
                {
                  publicId: 'up_atlassian',
                  name: 'atlassian',
                  displayName: 'Atlassian (mock)',
                  mcpUrl: 'https://example.test/mcp',
                  strategyType: 'mcp_spec',
                  strategySubMode: '',
                  requiresLink: true,
                  linkState: state.upstreamLinkState === 'CONNECTED' ? 2 : 1,
                  lastErrorReason: '',
                  lastErrorAt: '',
                },
              ],
            }),
          )
          return
        case 'StartConnect':
          // Pretend the OAuth dance is instant: flip the state then
          // hand back an in-app path so the SPA stays on /mcp-servers.
          state.upstreamLinkState = 'CONNECTED'
          await route.fulfill(rpcResponse({ redirectUrl: '/mcp-servers' }))
          return
        case 'Disconnect':
          state.upstreamLinkState = 'NONE'
          await route.fulfill(rpcResponse({}))
          return
        default:
          await route.fulfill({ status: 404, body: `unhandled RPC ${method}` })
      }
    })

    // Auto-accept the window.confirm before Disconnect.
    page.on('dialog', (d) => d.accept())

    // Step 1 — unauthenticated boot lands on /login.
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Welcome to Limen' })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Sign in with Zitadel' })).toBeVisible()

    // Step 2 — simulate the post-OIDC redirect by flipping the
    // authenticated flag and navigating straight to /mcp-servers.
    state.authenticated = true
    await page.goto('/mcp-servers')

    await expect(page.getByRole('heading', { name: 'MCP Servers' })).toBeVisible()
    const card = page.locator('[data-upstream-name="atlassian"]')
    await expect(card).toBeVisible()
    await expect(card.locator('[data-link-state]')).toHaveText('not connected')

    // Step 3 — Connect: StartConnect navigates the SPA to /mcp-servers
    // (our mock returns a relative path), which re-fetches and shows
    // the link as CONNECTED.
    await card.locator('[data-cta="connect"]').click()
    await expect(page.locator('[data-upstream-name="atlassian"] [data-link-state]')).toHaveText(
      'connected',
      { timeout: 10_000 },
    )

    // Step 4 — Disconnect (auto-accepted confirm) flips back to NONE.
    await page.locator('[data-upstream-name="atlassian"] [data-cta="disconnect"]').click()
    await expect(page.locator('[data-upstream-name="atlassian"] [data-link-state]')).toHaveText(
      'not connected',
      { timeout: 10_000 },
    )
  })
})
