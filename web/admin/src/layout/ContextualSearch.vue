<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Search } from '@lucide/vue'

type SearchMode = 'filter' | 'palette' | 'hidden'
type SearchMeta = { mode: SearchMode; placeholder?: string }

const route = useRoute()
const query = ref('')

const meta = computed<SearchMeta>(() => {
  const raw = (route.meta?.search ?? { mode: 'hidden' as const }) as SearchMeta
  return raw
})

const visible = computed(() => meta.value.mode !== 'hidden')
const placeholder = computed(
  () =>
    meta.value.placeholder ??
    (meta.value.mode === 'palette' ? 'Press / to open palette…' : 'Search…'),
)
const isPalette = computed(() => meta.value.mode === 'palette')
</script>

<template>
  <div v-if="visible" class="relative w-full max-w-md">
    <span
      class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant"
    >
      <Search :size="16" aria-hidden="true" />
    </span>
    <input
      v-model="query"
      type="search"
      :placeholder="placeholder"
      :aria-label="placeholder"
      class="w-full rounded-md border border-border-subtle bg-surface py-2 pl-9 pr-3 text-sm text-on-surface placeholder:text-secondary focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20"
    />
    <p
      v-if="isPalette"
      class="absolute right-3 top-1/2 hidden -translate-y-1/2 rounded border border-border-subtle bg-surface-container-low px-1.5 py-0.5 text-[10px] font-mono text-secondary md:block"
      title="Palette coming soon"
    >
      /
    </p>
  </div>
</template>
