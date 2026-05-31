---
name: frontend-e2e
description: Workflow for creating new Playwright end-to-end tests for the Limen frontend SPAs (portal + admin). Use when adding a new e2e test, fixing broken e2e tests, or extending existing test coverage. Always run this skill before writing any frontend e2e test code.
---

# Frontend E2E Testing — `web/**/e2e/` and `web/**/tests/e2e/`

Limen's frontend e2e tests are **UI rendering smoke tests with fully stubbed backends**.
No real Limen process, database, or OIDC provider runs during these tests.

**This skill must be loaded before writing or modifying any e2e test file.** The shared mock utilities and documented constraints are non-negotiable — skipping them leads to the same bugs this skill was created to prevent.

## Canonical References

| Artifact                                             | Purpose                                                     |
| ---------------------------------------------------- | ----------------------------------------------------------- |
| [`docs/frontend-e2e.md`](../../../docs/frontend-e2e.md)            | Architecture, constraints, running instructions             |
| [`web/shared/src/test-utils/e2e-mocks.ts`](../../../web/shared/src/test-utils/e2e-mocks.ts) | Reusable mock helpers (injectTenant, mockSessionFetch, interceptAuthLogin, rpc) |
| Existing admin tests                                           | Reference implementations                               |
| Existing portal test                                           | Reference for stateful page.route pattern                    |

---

## Architecture Recap

- **Playwright** drives headless Chromium. **vite preview** serves each SPA (portal :4173, admin :4174).
- All Connect-RPC calls are intercepted — **no real Limen backend** runs.
- OIDC is stubbed — authentication is simulated by toggling mock state.
- Each Playwright config declares its own `webServer` that builds + previews the SPA.
- **Relative asset paths** (`base: './'`) mean the SPA must be bootstrapped at a 1-segment URL (`/`, `/mcp-servers`), not at deep paths like `/mcp-servers/new`.

---

## When to Activate This Skill

Invoke this skill proactively whenever you:

| Trigger                                             | Action                                                                                                                  |
| --------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| **Creating a new e2e test**                             | Follow the "Creating a New Test" workflow below before writing a single line.                                             |
| **Adding a new test case to an existing spec**          | Re-read the constraints section — same rules apply.                                                                       |
| **Fixing a broken e2e test**                            | Diagnose with the "Troubleshooting" checklist first; don't guess.                                                         |
| **Adding a new page/feature that needs e2e coverage**   | Check that the new page exposes `data-testid` attributes (this is the component author's responsibility, but flag missing ones). |
| **Modifying `playwright.config.ts` or `vite.config.ts`** | Re-run the constraints checklist — base paths and preview commands are tightly coupled.                                    |
| **Adding a new SPIKE or service that the SPA calls**     | Ensure the mock route handler covers the new method.                                                                      |

---

## Step-by-Step: Creating a New E2e Test

### Step 0 — Load Docs

**Before writing ANY code**, read:
1. `docs/frontend-e2e.md` — the architecture and constraints
2. `web/shared/src/test-utils/e2e-mocks.ts` — what shared helpers are available
3. An existing test in the same SPA as a template

### Step 1 — Choose the Mocking Approach

**Approach 1: fetch override (use for admin)** — When `page.route` with regex is unreliable
for Connect-RPC POST requests. This is the preferred approach for admin tests.
Import the shared helpers:

```typescript
import { readFileSync } from 'node:fs'
import {
  TENANT,
  ADMIN_SESSION,
  injectTenant,
  mockSessionFetch,
  interceptAuthLogin,
  rpc,
} from '<relative-path-to>/shared/src/test-utils/e2e-mocks'
```

**Approach 2: page.route with regex (use for portal)** — When the mock needs to be
stateful (flipping `authenticated` flag between test steps) and there's no
redirect-loop issue. Import only base utilities:

```typescript
import { test, expect } from '@playwright/test'
import { TENANT, injectTenant, rpc } from '<path>/e2e-mocks'
```

### Step 2 — Write the Test Boilerplate

For **admin** tests (Approach 1):

```typescript
const INDEX_HTML = readFileSync('dist/index.html', 'utf-8')

test.describe('feature name (mocked services)', () => {
  test('does the thing', async ({ page, context }) => {
    // 1. Inject tenant
    await injectTenant(context)

    // 2. Mock session (window.fetch override)
    await mockSessionFetch(context)

    // 3. Intercept /auth/login redirects
    await interceptAuthLogin(page, INDEX_HTML)

    // 4. Add feature-specific route interceptors
    await page.route(`**/t/${TENANT}/api/**`, async (route) => {
      const path = route.request().url().split(`/t/${TENANT}/api/`)[1]
      if (path === 'limen.portal.v1.PortalService/SomeMethod') {
        return route.fulfill(rpc({ /* response */ }))
      }
      return route.fulfill({ status: 404, body: `unhandled: ${path}` })
    })

    // 5. Navigate and assert
    await page.goto('/target-path')
    await expect(page.getByRole('heading', { level: 1, name: /Expected/ })).toBeVisible()
  })
})
```

For **portal** tests (Approach 2):

```typescript
test.describe('feature name (stubbed OIDC + RPC)', () => {
  test('does the thing', async ({ page, context }) => {
    await injectTenant(context)

    const state: { authenticated: boolean; /* ... */ } = { authenticated: false }

    // Auth login page (simple HTML, not SPA)
    await page.route('**/auth/login**', async (route) => {
      if (route.request().method() !== 'GET') return route.continue()
      return route.fulfill({
        status: 200,
        contentType: 'text/html',
        body: '<html><body><h1>Welcome to Limen</h1></body></html>',
      })
    })

    // Stateful session: 501 when !authenticated, valid session when true
    await page.route(/\/t\/acme\/api\/limen\.session\.v1\.SessionService\//, async (route) => {
      const url = route.request().url()
      if (url.includes('/GetSession')) {
        if (!state.authenticated) {
          return route.fulfill({ status: 501, contentType: 'application/json',
            body: JSON.stringify({ code: 'unimplemented' }) })
        }
        return route.fulfill(rpc({ user: { ... }, tenant: { ... }, role: 'ROLE_OWNER' }))
      }
      return route.fulfill(rpc({}))
    })

    // Feature-specific route interceptors...
    await page.route(/regex-for-api-prefix/, async (route) => { /* ... */ })

    // Test: unauthenticated
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Welcome to Limen' })).toBeVisible()

    // Test: authenticated
    state.authenticated = true
    await page.goto('/protected-page')
    // ... assertions ...
  })
})
```

### Step 3 — Add Route Interceptors for the Service Under Test

For admin tests, intercept `**/t/${TENANT}/api/**` (glob pattern). Parse the path
from the URL and dispatch by method name:

```typescript
await page.route(`**/t/${TENANT}/api/**`, async (route) => {
  const path = route.request().url().split(`/t/${TENANT}/api/`)[1]
  if (!path) return route.fulfill({ status: 400, body: 'bad request' })

  // PortalService methods
  if (path === 'limen.portal.v1.PortalService/ListUpstreams') {
    return route.fulfill(rpc({ upstreams: [...] }))
  }

  // AdminService methods
  if (path === 'limen.admin.v1.AdminService/DeleteUpstream') {
    return route.fulfill(rpc({}))
  }

  return route.fulfill({ status: 404, body: `unhandled: ${path}` })
})
```

For portal tests, use the same regex prefix as the session but for `PortalService`.

### Step 4 — Navigate and Assert (Respecting Constraints)

**DO:**
- Navigate to 1-segment URLs (`/`, `/mcp-servers`) — never to `/a/b/c` directly
- Use client-side nav for deep routes (clicking `router-link` buttons)
- Use `getByTestId()` for form fields, buttons, modals
- Use `getByRole('heading', { name: /.../ })` for page headings
- Interact with custom modal components via `data-testid` (not `page.on('dialog')`)

**DON'T:**
- Use `page.on('dialog')` — Limen uses custom modal components, not `window.confirm()`
- Navigate to 3+ segment deep URLs directly — relative assets will 404
- Put numeric enum values in mock responses — use string enums like `'ROLE_ADMIN'`

### Step 5 — Run and Verify

```bash
cd web/admin    # or web/portal
npx playwright test --reporter=line
```

Fix any failures BEFORE committing. Never push an untested e2e change.

## Key Constraints (Must Be Respected)

### 1. vite preview base path

**Playwright config MUST pass `--base ./`:**

```ts
webServer: {
  command: 'pnpm run build && pnpm run preview --host 127.0.0.1 --port 4174 --base ./',
}
```

Without it, `vite preview` uses the dev base path (`/__vite/admin/`) and all
`page.goto()` calls hit a "did you mean to visit..." error page.

### 2. Relative asset path depth limit

SPAs built with `base: './'` cannot load at URLs deeper than 1 segment.
`./assets/index.js` resolved from `/mcp-servers/new` → `/mcp-servers/assets/...` (404).

**Always bootstrap at a 1-segment URL** and client-nav deeper:

```typescript
// ✅ Correct
await page.goto('/mcp-servers')
await page.getByTestId('add-upstream').click() // router.push to /mcp-servers/new

// ❌ Wrong
await page.goto('/mcp-servers/new') // assets 404
```

### 3. Protobuf-JSON enum format

`@bufbuild/protobuf` v2 deserializes enums by name. **Always use string enums:**

```typescript
// ✅ Correct
{ role: 'ROLE_ADMIN' }
// ❌ Wrong
{ role: 3 }
```

### 4. Custom modal components

Limen uses `ConfirmDeleteModal`, `SuccessModal`, `ErrorModal` from `@limen/shared`.
Interact via `data-testid`, never via `page.on('dialog')`:

```typescript
// ✅ Correct
await page.getByTestId('confirm-delete-input').fill('identifier-text')
await page.getByTestId('confirm-delete-confirm').click()

// ❌ Wrong
page.once('dialog', (d) => d.accept())
```

### 5. Window fetch override for session (admin only)

When the SPA reboots through `/auth/login`, `page.route` regex patterns may not
intercept Connect-RPC POST requests reliably. Use `mockSessionFetch(context)` instead.

## Troubleshooting Checklist

When an e2e test fails, check in this order:

- [ ] Does `playwright.config.ts` have `--base ./` in the preview command?
- [ ] Is `window.__LIMEN_TENANT__` injected via `injectTenant(context)` or `addInitScript`?
- [ ] Is the session mock returning string enums (`'ROLE_ADMIN'`, not `3`)?
- [ ] For admin tests: is `mockSessionFetch(context)` called before `page.goto()`?
- [ ] For admin tests: is `interceptAuthLogin(page, html)` registered before `page.goto()`?
- [ ] Is the page being navigated to at a depth ≤ 1 segment? If deeper, are you client-navving?
- [ ] Are you using `getByTestId()` for modal interactions (not `page.on('dialog')`)?
- [ ] Are all Connect-RPC methods the page calls intercepted by a route handler?
- [ ] Does the route handler return `application/json` content type?
- [ ] Are response bodies valid protobuf JSON (correct field names, snake_case, string enums)?

## File Locations

| What                     | Where                                                    |
| ------------------------ | -------------------------------------------------------- |
| Admin e2e tests          | `web/admin/tests/e2e/*.spec.ts`                            |
| Portal e2e tests         | `web/portal/e2e/*.spec.ts` and `web/e2e/*.spec.ts`         |
| Shared mock utilities    | `web/shared/src/test-utils/e2e-mocks.ts`                   |
| Admin Playwright config  | `web/admin/playwright.config.ts`                           |
| Portal Playwright config | `web/portal/playwright.config.ts`                          |
| Architecture guide       | `docs/frontend-e2e.md`                                     |
| Nix devshell             | `flake.nix` (chromium + playwright-driver packages)       |
| CI workflow              | `.github/workflows/ci.yml` (`frontend-e2e-admin`, `frontend-e2e-portal`) |

## Adherence Checklist

Before completing any e2e task, verify:

- [ ] Shared helpers (`e2e-mocks.ts`) are imported — no inline copy-paste of fetch override or auth interceptor
- [ ] Playwright config includes `--base ./` in the webServer preview command
- [ ] No `page.on('dialog')` usage exists (or if it does, the component actually uses `window.confirm()`)
- [ ] All enum values in mock responses are strings (not numbers)
- [ ] No `page.goto()` targets URLs deeper than 1 segment
- [ ] All Connect-RPC calls the page makes are intercepted
- [ ] Mock responses use `application/json` content type
- [ ] Test was run locally and passed BEFORE being committed
- [ ] New test file is in the correct directory per the "File Locations" table
