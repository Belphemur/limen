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

useThemeStore().init()

app.mount('#app')
