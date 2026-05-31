import { defineConfig, devices } from '@playwright/test'

// Playwright runs against the *built* SPA served by `vite preview`,
// because that's what the static host (Cloudflare Pages / Caddy)
// ships in production. No Limen process is required — every
// Connect-RPC call is intercepted at the network layer (see
// e2e/portal.spec.ts) and the OIDC redirect is stubbed by setting
// the portal-session boot state directly via a route handler.
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? 'list' : 'list',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  webServer: {
    command: 'pnpm run build && pnpm run preview --host 127.0.0.1 --port 4173 --base ./',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
