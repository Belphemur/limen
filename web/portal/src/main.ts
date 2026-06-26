import { createApp } from 'vue'
import { createPinia } from 'pinia'

import { setSessionTransport } from '@limen/shared/session'
import { setBillingTransport, setBillingService, useBillingStore } from '@limen/shared/billing'

import '@fontsource-variable/inter'
import '@fontsource-variable/outfit'

import App from './App.vue'
import { createRouter } from './router'
import { useThemeStore } from './stores/theme'
import { portalTransport } from './api/client'
import { BillingService } from '@gen/limen/portal/v1/portal_pb.ts'
import './styles/main.css'

// The shared session store reads its Connect transport from a module
// pin set BEFORE the router guard runs. The portal SPA's transport
// (cookie-bearing, tenant-derived baseUrl) doubles as the
// SessionService transport — same /t/{tenant}/api/ mount.
setSessionTransport(portalTransport())

// Same transport serves BillingService (also mounted at
// /t/{tenant}/api/limen.portal.v1.BillingService/). Pinning it here
// keeps the billing store decoupled from the portal client module —
// the store only ever sees the Transport, never the URL.
setBillingTransport(portalTransport())
setBillingService(BillingService)

const app = createApp(App)
app.use(createPinia())
app.use(createRouter())

// Sync the theme store with whatever the no-flash boot script in index.html
// already applied to <html>. Idempotent; safe if the script didn't run (SSR).
useThemeStore().init()

// Fire-and-forget initial billing fetch. The store swallows errors
// into its own `error` ref (a failed summary fetch must never crash
// the SPA), and the response-header interceptor will re-trigger this
// on the next gated request once the server stamps a grace signal.
void useBillingStore().fetchBillingSummary()

app.mount('#app')
