import { createApp } from 'vue'
import { createPinia } from 'pinia'

import { createConnectTransport } from '@connectrpc/connect-web'
import { setSessionTransport } from '@limen/shared/session'

import '@fontsource-variable/inter'
import '@fontsource-variable/outfit'

import App from './App.vue'
import { createRouter } from './router'
import { useThemeStore } from './stores/theme'
import './styles/main.css'

// Build the cookie-bearing Connect transport for the SessionService.
// Limen mounts every SPA's APIs under /t/<tenant>/api/, so we lift
// the tenant slug off the URL path. In vite dev mode this falls back
// to "dev"; the bootstrap call will fail and the router guard will
// redirect to /auth/login, which is the intended behaviour.
function discoverTenant(): string {
  const w = window as Window & { __LIMEN_TENANT__?: string }
  if (w.__LIMEN_TENANT__) return w.__LIMEN_TENANT__
  const match = window.location.pathname.match(/^\/t\/([^/]+)\//)
  return match ? match[1] : 'dev'
}

setSessionTransport(
  createConnectTransport({
    baseUrl: `${window.location.origin}/t/${discoverTenant()}/api`,
    fetch: (input, init) => globalThis.fetch(input, { ...init, credentials: 'include' }),
  }),
)

const app = createApp(App)
app.use(createPinia())
app.use(createRouter())

useThemeStore().init()

app.mount('#app')
