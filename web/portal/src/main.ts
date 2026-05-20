import { createApp } from 'vue'
import { createPinia } from 'pinia'

import '@fontsource-variable/inter'
import '@fontsource-variable/outfit'

import App from './App.vue'
import { createRouter } from './router'
import { useThemeStore } from './stores/theme'
import './styles/main.css'

const app = createApp(App)
app.use(createPinia())
app.use(createRouter())

// Sync the theme store with whatever the no-flash boot script in index.html
// already applied to <html>. Idempotent; safe if the script didn't run (SSR).
useThemeStore().init()

app.mount('#app')
