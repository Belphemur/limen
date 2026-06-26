import { createApp } from 'vue'
import { createPinia } from 'pinia'

import { createConnectTransport } from '@connectrpc/connect-web'
import { setSessionTransport } from '@limen/shared/session'
import { setBillingTransport, useBillingStore } from '@limen/shared/billing'

import '@fontsource-variable/outfit'

import App from './App.vue'
import { createRouter } from './router'
import { useThemeStore } from './stores/theme'
import { setAdminTransport, setSignupTransport } from '@/transport/adminClient'
import './styles/main.css'

// Build the cookie-bearing Connect transports for SessionService,
// AdminService + PortalService (per-tenant) and SignupService (root).
// Per-tenant services are multiplexed onto /t/<tenant>/api/ via
// http.ServeMux; SignupService lives at /api/ on the root router.
function discoverTenant(): string {
  const w = window as Window & { __LIMEN_TENANT__?: string }
  if (w.__LIMEN_TENANT__) return w.__LIMEN_TENANT__
  const match = window.location.pathname.match(/^\/t\/([^/]+)\//)
  return match ? match[1] : 'dev'
}

const cookieFetch = (input: RequestInfo | URL, init?: RequestInit) =>
  globalThis.fetch(input, { ...init, credentials: 'include' })

const perTenantTransport = createConnectTransport({
  baseUrl: `${window.location.origin}/t/${discoverTenant()}/api`,
  fetch: cookieFetch,
})

setSessionTransport(perTenantTransport)
setBillingTransport(perTenantTransport)
setAdminTransport(perTenantTransport)

setSignupTransport(
  createConnectTransport({
    baseUrl: `${window.location.origin}/api`,
    fetch: cookieFetch,
  }),
)

const app = createApp(App)
app.use(createPinia())
app.use(createRouter())

useThemeStore().init()

// Fire-and-forget initial billing fetch. The store swallows errors
// into its own `error` ref, and the response-header interceptor
// installed in adminClient will re-trigger this on the next gated
// request once the server stamps a grace signal.
void useBillingStore().fetchBillingSummary()

app.mount('#app')
