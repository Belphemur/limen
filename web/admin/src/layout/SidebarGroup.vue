<script setup lang="ts">
import { ref, computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { ChevronDown } from '@lucide/vue'
import type { NavLeaf } from '@/router/routes'
import type { Component } from 'vue'

const props = defineProps<{
  label: string
  icon: Component
  children: NavLeaf[]
}>()

const route = useRoute()

// A group is "auto-open" when any child route is active, but the user
// can also toggle it manually. Default to open so the destination is
// visible on first paint.
const containsActive = computed(() => props.children.some((c) => route.path.startsWith(c.path)))
const open = ref(true)
</script>

<template>
  <div class="space-y-1">
    <button
      type="button"
      class="flex w-full items-center justify-between rounded-lg px-4 py-3 text-sidebar-fg-muted transition-colors hover:bg-sidebar-item-hover-bg hover:text-sidebar-fg"
      :aria-expanded="open"
      @click="open = !open"
    >
      <span class="flex items-center gap-3">
        <component :is="icon" :size="20" aria-hidden="true" />
        <span class="text-sm font-medium">{{ label }}</span>
      </span>
      <ChevronDown
        :size="16"
        aria-hidden="true"
        class="transition-transform duration-200"
        :class="{ 'rotate-180': open }"
      />
    </button>
    <div v-show="open || containsActive" class="space-y-1 pl-11 pr-4">
      <RouterLink
        v-for="child in children"
        :key="child.path"
        :to="child.path"
        class="flex items-center gap-3 rounded-lg px-3 py-2 text-sm transition-colors"
        :class="
          route.path === child.path
            ? 'text-sidebar-fg'
            : 'text-sidebar-fg-muted hover:text-sidebar-fg'
        "
      >
        <component :is="child.icon" :size="16" aria-hidden="true" />
        <span>{{ child.label }}</span>
      </RouterLink>
    </div>
  </div>
</template>
