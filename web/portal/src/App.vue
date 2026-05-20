<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterView, RouterLink, useRoute } from 'vue-router'
import { LogOut, User } from '@lucide/vue'
import { useSessionStore } from '@/stores/session'
import ThemeSwitcher from '@/components/ThemeSwitcher.vue'
import logoUrl from '@/assets/limen-logo.svg'

const session = useSessionStore()
const route = useRoute()
const menuOpen = ref(false)

onMounted(() => {
  void session.refresh()
})

function closeMenu() {
  menuOpen.value = false
}
</script>

<template>
  <div class="flex min-h-screen flex-col bg-bg-main text-on-surface">
    <header
      class="sticky top-0 z-topbar border-b border-border-subtle bg-surface backdrop-blur supports-[backdrop-filter]:bg-surface/90">
      <div class="mx-auto flex h-portal-header max-w-6xl items-center justify-between px-4">
        <RouterLink to="/" class="flex items-center gap-2 font-display text-lg font-semibold tracking-tight">
          <img :src="logoUrl" alt="" aria-hidden="true" width="28" height="28" class="h-7 w-7 rounded-md" />
          <span>Limen Portal</span>
        </RouterLink>

        <nav v-if="session.authenticated" class="flex gap-1 text-sm font-medium">
          <RouterLink to="/"
            class="rounded-md px-3 py-1.5 text-on-surface-variant transition-colors hover:bg-surface-container-low hover:text-on-surface"
            :class="route.path === '/' ? 'bg-surface-container-low text-on-surface' : ''">
            Dashboard
          </RouterLink>
          <RouterLink to="/mcp-servers"
            class="rounded-md px-3 py-1.5 text-on-surface-variant transition-colors hover:bg-surface-container-low hover:text-on-surface"
            :class="route.path.startsWith('/mcp-servers')
              ? 'bg-surface-container-low text-on-surface'
              : ''
              ">
            MCP Servers
          </RouterLink>
          <RouterLink to="/mcp-clients"
            class="rounded-md px-3 py-1.5 text-on-surface-variant transition-colors hover:bg-surface-container-low hover:text-on-surface"
            :class="route.path.startsWith('/mcp-clients')
              ? 'bg-surface-container-low text-on-surface'
              : ''
              ">
            MCP Clients
          </RouterLink>
          <RouterLink to="/settings"
            class="rounded-md px-3 py-1.5 text-on-surface-variant transition-colors hover:bg-surface-container-low hover:text-on-surface"
            :class="route.path.startsWith('/settings')
              ? 'bg-surface-container-low text-on-surface'
              : ''
              ">
            Settings
          </RouterLink>
        </nav>

        <div v-if="session.authenticated" class="relative flex items-center gap-3">
          <ThemeSwitcher />
          <button type="button"
            class="inline-flex cursor-pointer items-center gap-2 rounded-md border border-border-subtle bg-surface px-2.5 py-1.5 text-sm text-on-surface-variant hover:text-on-surface focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
            :aria-expanded="menuOpen" aria-haspopup="menu" @click="menuOpen = !menuOpen">
            <User :size="16" aria-hidden="true" />
            <span class="max-w-40 truncate text-xs">{{ session.user?.email }}</span>
          </button>
          <div v-if="menuOpen"
            class="absolute right-0 top-full z-dropdown mt-2 w-48 overflow-hidden rounded-lg border border-border-subtle bg-surface shadow-lg"
            role="menu" @click="closeMenu">
            <button type="button" role="menuitem"
              class="flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left text-sm text-on-surface hover:bg-surface-container-low"
              @click="session.logout()">
              <LogOut :size="16" aria-hidden="true" />
              Sign out
            </button>
          </div>
        </div>
      </div>
    </header>

    <main class="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
      <RouterView />
    </main>
  </div>
</template>
