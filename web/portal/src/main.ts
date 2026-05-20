import { createApp } from 'vue'
import { createPinia } from 'pinia'

import { setSessionTransport } from '@limen/shared/session'

import '@fontsource-variable/inter'
import '@fontsource-variable/outfit'

import App from './App.vue'
import { createRouter } from './router'
import { useThemeStore } from './stores/theme'
import { portalTransport } from './api/client'
import './styles/main.css'

// The shared session store reads its Connect transport from a module
// pin set BEFORE the router guard runs. The portal SPA's transport
// (cookie-bearing, tenant-derived baseUrl) doubles as the
// SessionService transport — same /t/{tenant}/api/ mount.
setSessionTransport(portalTransport())

const app = createApp(App)
app.use(createPinia())
app.use(createRouter())

// Sync the theme store with whatever the no-flash boot script in index.html
// already applied to <html>. Idempotent; safe if the script didn't run (SSR).
useThemeStore().init()

app.mount('#app')
