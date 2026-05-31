# Frontend E2E Testing Guide

## Architecture

Limen's frontend e2e tests are **UI rendering smoke tests with fully stubbed backends**.
No real Limen process, database, or OIDC provider runs during these tests.

- **Playwright** drives a headless Chromium browser.
- **vite preview** serves each SPA at a dedicated port (portal :4173, admin :4174).
- All Connect-RPC calls are intercepted via `page.route()` or `window.fetch` overrides.
- OIDC is completely stubbed — authentication is simulated by toggling mock state.

## Two Approaches to Mocking

### Approach 1: fetch override (admin tests)

Used when `page.route` with regex patterns is unreliable for intercepting
Connect-RPC POST requests from within the SPA's JavaScript.

```typescript
import { injectTenant, mockSessionFetch, interceptAuthLogin } from '<path>/e2e-mocks'

await injectTenant(context)
await mockSessionFetch(context)       // overrides window.fetch for GetSession
await interceptAuthLogin(page, html)  // serves SPA HTML at /auth/login
```

### Approach 2: page.route with regex (portal tests)

Used for simpler auth flows without redirect loops. Works well when
the mock can be stateful (flipping authenticated flag between steps).

```typescript
await page.route(/\/t\/acme\/api\/limen\.session\.v1\.SessionService\//, async (route) => {
  if (!state.authenticated) return route.fulfill({ status: 501, ... })
  return route.fulfill(rpcResponse({ user: ..., tenant: ..., role: ... }))
})
```

## Key Constraints

### vite preview base path

Both SPAs build with `base: './'` (relative assets). `vite preview` must
also use `--base ./` or it defaults to a dev path (`/__vite/admin/`).
Playwright configs include `--base ./` in their webServer command:

```ts
webServer: {
  command: 'pnpm run build && pnpm run preview --host 127.0.0.1 --port 4174 --base ./',
}
```

### Relative asset paths at depth

When `base: './'`, relative asset URLs (`./assets/index.js`) resolve
relative to the current URL. A direct `page.goto('/mcp-servers/new')`
resolves assets to `/mcp-servers/assets/...` (404). **Always bootstrap
the SPA at a one-segment URL** (`/`, `/mcp-servers`) and use
client-side navigation for deeper routes.

```typescript
// ✅ Correct: bootstrap at shallow URL, client-nav deeper
await page.goto('/mcp-servers')
await page.getByTestId('add-upstream').click()  // router.push to /mcp-servers/new
```

### Protobuf-JSON format

@bufbuild/protobuf v2 uses string-valued enums in JSON. Session mocks
must return enums as strings, not numbers:

```json
// ✅ Correct
{ "role": "ROLE_ADMIN" }
// ❌ Wrong
{ "role": 3 }
```

### Custom modals, not native dialogs

Limen uses custom modal components (`ConfirmDeleteModal`, `SuccessModal`,
`ErrorModal`) from `@limen/shared`. Tests must interact with these via
`data-testid` attributes, not `page.on('dialog')`:

```typescript
// ✅ Correct
await page.getByTestId('confirm-delete-input').fill('demo')
await page.getByTestId('confirm-delete-confirm').click()
// ❌ Wrong
page.once('dialog', (d) => d.accept())
```

## Running Tests

```bash
# Admin e2e
cd web/admin && npx playwright test

# Portal e2e
cd web/portal && npx playwright test

# Install browsers (first run)
cd web/admin && npx playwright install --with-deps chromium
```

## Nix Shell

Playwright + Chromium are available in the Nix devshell (see flake.nix).
Set `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1` and `PLAYWRIGHT_BROWSERS_PATH`
to use Nix-provided browsers instead of downloading them.

## CI

E2e tests run in GitHub Actions via the `frontend-e2e-admin` and
`frontend-e2e-portal` jobs (see `.github/workflows/ci.yml`).
