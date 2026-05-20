<script setup lang="ts">
import { ref } from 'vue'
import { Bell, Settings as SettingsIcon, User, LogOut, ExternalLink } from '@lucide/vue'
import ThemeSwitcher from '@/components/ThemeSwitcher.vue'
import ContextualSearch from './ContextualSearch.vue'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const menuOpen = ref(false)
</script>

<template>
  <header
    class="fixed left-0 right-0 top-0 z-topbar flex h-header-height items-center gap-4 border-b border-border-subtle bg-surface px-gutter md:left-sidebar-width"
  >
    <div class="flex-1">
      <ContextualSearch />
    </div>

    <nav class="hidden items-center gap-2 md:flex">
      <a
        href="https://limen.dev/docs"
        target="_blank"
        rel="noreferrer noopener"
        class="rounded-md px-3 py-1.5 text-sm font-medium text-on-surface-variant transition-colors hover:bg-surface-container-low hover:text-on-surface"
      >
        Docs
      </a>
      <a
        href="https://status.limen.dev"
        target="_blank"
        rel="noreferrer noopener"
        class="inline-flex items-center gap-1 rounded-md px-3 py-1.5 text-sm font-medium text-on-surface-variant transition-colors hover:bg-surface-container-low hover:text-on-surface"
      >
        API Status
        <ExternalLink :size="14" aria-hidden="true" />
      </a>
    </nav>

    <div class="flex items-center gap-2">
      <button
        type="button"
        aria-label="Notifications"
        class="inline-flex h-10 w-10 cursor-pointer items-center justify-center rounded-full text-on-surface-variant transition-colors hover:bg-surface-container-low hover:text-on-surface"
      >
        <Bell :size="20" aria-hidden="true" />
      </button>
      <ThemeSwitcher />
      <div class="relative">
        <button
          type="button"
          class="inline-flex cursor-pointer items-center gap-2 rounded-md border border-border-subtle bg-surface px-2.5 py-1.5 text-sm text-on-surface-variant hover:text-on-surface focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
          :aria-expanded="menuOpen"
          aria-haspopup="menu"
          @click="menuOpen = !menuOpen"
        >
          <User :size="16" aria-hidden="true" />
          <span class="max-w-40 truncate text-xs">{{ session.user?.email ?? 'Loading…' }}</span>
        </button>
        <div
          v-if="menuOpen"
          role="menu"
          class="absolute right-0 top-full z-dropdown mt-2 w-56 overflow-hidden rounded-lg border border-border-subtle bg-surface shadow-lg"
          @click="menuOpen = false"
        >
          <div class="px-3 py-2 text-xs text-secondary">
            {{ session.tenant?.name ?? '' }}
          </div>
          <button
            type="button"
            role="menuitem"
            class="flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left text-sm text-on-surface hover:bg-surface-container-low"
            @click="session.logout()"
          >
            <LogOut :size="16" aria-hidden="true" />
            Sign out
          </button>
        </div>
      </div>
      <button
        type="button"
        aria-label="Settings"
        class="inline-flex h-10 w-10 cursor-pointer items-center justify-center rounded-full text-on-surface-variant transition-colors hover:bg-surface-container-low hover:text-on-surface md:hidden"
      >
        <SettingsIcon :size="20" aria-hidden="true" />
      </button>
    </div>
  </header>
</template>
