<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterView, RouterLink, useRoute } from 'vue-router'
import { useSessionStore } from './stores/session'

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
          <RouterLink to="/" :class="route.path === '/' ? 'font-semibold' : ''"
            >Dashboard</RouterLink
          >
          <RouterLink
            to="/upstreams"
            :class="route.path.startsWith('/upstreams') ? 'font-semibold' : ''"
            >Upstreams</RouterLink
          >
          <RouterLink
            to="/mcp-clients"
            :class="route.path.startsWith('/mcp-clients') ? 'font-semibold' : ''"
            >MCP Clients</RouterLink
          >
          <RouterLink
            to="/settings"
            :class="route.path.startsWith('/settings') ? 'font-semibold' : ''"
            >Settings</RouterLink
          >
        </nav>
        <span v-if="session.authenticated" class="text-sm text-slate-500">{{
          session.user?.email
        }}</span>
      </div>
    </header>
    <main class="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
      <RouterView />
    </main>
  </div>
</template>
