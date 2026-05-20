<script setup lang="ts">
import { Construction } from '@lucide/vue'

defineProps<{
  title?: string
}>()
</script>

<template>
  <section
    class="flex flex-col items-center justify-center rounded-lg border-2 border-dashed border-border-subtle bg-surface p-12 text-center text-secondary"
  >
    <Construction :size="48" aria-hidden="true" class="mb-4 opacity-50" />
    <h2 class="font-display text-xl font-semibold text-on-surface">
      {{ title ?? 'Coming soon' }}
    </h2>
    <p class="mt-2 max-w-md text-sm">See Phase 9c — this route is part of a later milestone.</p>
  </section>
</template>
