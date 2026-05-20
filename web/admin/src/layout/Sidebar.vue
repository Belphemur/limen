<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { Plus, Settings, LogOut } from '@lucide/vue'
import logoUrl from '@/assets/limen-logo.svg'
import { useSessionStore } from '@/stores/session'
import { navTree, ROUTES } from '@/router/routes'
import SidebarGroup from './SidebarGroup.vue'

const route = useRoute()
const session = useSessionStore()

const isActive = (path: string) => route.path === path
</script>

<template>
  <aside
    class="fixed inset-y-0 left-0 z-sidebar hidden w-sidebar-width flex-col bg-sidebar-bg text-sidebar-fg md:flex"
    aria-label="Primary"
  >
    <!-- Brand -->
    <div class="flex items-center gap-3 px-6 py-5">
      <img
        :src="logoUrl"
        alt=""
        aria-hidden="true"
        width="32"
        height="32"
        class="h-8 w-8 rounded-md bg-primary-container"
      />
      <div class="flex flex-col leading-tight">
        <span class="font-display text-base font-semibold text-sidebar-fg">Limen Admin</span>
        <span class="text-xs text-sidebar-fg-muted">Enterprise Control</span>
      </div>
    </div>

    <!-- Nav — rendered from the central navTree in router/routes.ts -->
    <nav class="flex flex-1 flex-col gap-1 overflow-y-auto px-3">
      <template v-for="node in navTree" :key="node.label">
        <RouterLink
          v-if="node.kind === 'leaf'"
          :to="node.path"
          class="flex items-center gap-3 rounded-lg px-4 py-3 transition-colors"
          :class="
            isActive(node.path)
              ? 'bg-primary text-white'
              : 'text-sidebar-fg-muted hover:bg-sidebar-item-hover-bg hover:text-sidebar-fg'
          "
        >
          <component :is="node.icon" :size="20" aria-hidden="true" />
          <span class="text-sm font-medium">{{ node.label }}</span>
        </RouterLink>
        <SidebarGroup v-else :label="node.label" :icon="node.icon" :children="node.children" />
      </template>
    </nav>

    <!-- Footer -->
    <div class="mt-auto border-t border-sidebar-divider p-3">
      <RouterLink
        :to="ROUTES.mcpServerNew"
        class="mb-2 flex w-full items-center justify-center gap-2 rounded-lg bg-primary-container px-4 py-3 text-sm font-medium text-white shadow-sm transition-colors hover:bg-primary"
      >
        <Plus :size="16" aria-hidden="true" />
        Add New Server
      </RouterLink>
      <a
        href="https://limen.dev/docs/support"
        target="_blank"
        rel="noreferrer noopener"
        class="flex items-center gap-3 rounded-lg px-4 py-2 text-sm text-sidebar-fg-muted transition-colors hover:bg-sidebar-item-hover-bg hover:text-sidebar-fg"
      >
        <Settings :size="16" aria-hidden="true" />
        Support
      </a>
      <button
        type="button"
        class="flex w-full items-center gap-3 rounded-lg px-4 py-2 text-sm text-sidebar-fg-muted transition-colors hover:bg-sidebar-item-hover-bg hover:text-sidebar-fg"
        @click="session.logout()"
      >
        <LogOut :size="16" aria-hidden="true" />
        Sign Out
      </button>
    </div>
  </aside>
</template>
