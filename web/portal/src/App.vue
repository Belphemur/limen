<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterView, RouterLink, useRoute } from 'vue-router'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const route = useRoute()

onMounted(() => {
  void session.refresh()
})
</script>

<template>
  <div class="flex min-h-screen flex-col">
    <header class="border-b border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-800">
      <div class="mx-auto flex max-w-6xl items-center justify-between px-4 py-3">
        <RouterLink to="/" class="text-lg font-semibold">Limen Portal</RouterLink>
        <nav v-if="session.authenticated" class="flex gap-4 text-sm">
          <RouterLink to="/" :class="route.path === '/' ? 'font-semibold' : ''">Dashboard</RouterLink>
          <RouterLink to="/mcp-servers" :class="route.path.startsWith('/mcp-servers') ? 'font-semibold' : ''">MCP
            Servers
          </RouterLink>
          <RouterLink to="/mcp-clients" :class="route.path.startsWith('/mcp-clients') ? 'font-semibold' : ''">MCP
            Clients</RouterLink>
          <RouterLink to="/settings" :class="route.path.startsWith('/settings') ? 'font-semibold' : ''">Settings
          </RouterLink>
        </nav>
        <div v-if="session.authenticated" class="flex items-center gap-4 text-sm">
          <span class="text-slate-500">{{ session.user?.email }}</span>
          <button type="button"
            class="cursor-pointer rounded-md border border-slate-300 px-2 py-1 text-xs font-medium text-slate-600 hover:border-rose-400 hover:text-rose-600 dark:border-slate-600 dark:text-slate-300"
            @click="session.logout()">
            Sign out
          </button>
        </div>
      </div>
    </header>
    <main class="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
      <RouterView />
    </main>
  </div>
</template>
