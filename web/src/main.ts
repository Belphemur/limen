import { createApp } from 'vue'
import { createPinia } from 'pinia'

import App from './App.vue'
import { createRouter } from './router'
import './styles/main.css'

const app = createApp(App)
app.use(createPinia())
app.use(createRouter())
app.mount('#app')
