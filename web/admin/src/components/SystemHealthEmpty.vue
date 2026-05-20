<script setup lang="ts">
import { Monitor } from '@lucide/vue'
</script>

<template>
  <section
    class="flex h-full flex-col items-center justify-center rounded-lg border-2 border-dashed border-border-subtle bg-surface p-8 text-center"
    aria-labelledby="system-health-title"
  >
    <Monitor :size="48" aria-hidden="true" class="mb-4 text-on-surface-variant opacity-50" />
    <h3 id="system-health-title" class="font-display text-lg font-semibold text-on-surface">
      System Health
    </h3>
    <p class="mt-2 max-w-sm text-sm text-on-surface-variant">
      Waiting for data… metrics will appear here once an upstream is connected and clients begin
      issuing calls.
    </p>
  </section>
</template>
