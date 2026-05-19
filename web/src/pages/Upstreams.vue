<script setup lang="ts">
import { onMounted } from 'vue'
import { useUpstreamsStore } from '../stores/upstreams'

const upstreams = useUpstreamsStore()

onMounted(() => {
  void upstreams.refresh()
})
</script>

<template>
  <section>
    <h1 class="text-2xl font-semibold">Upstreams</h1>
    <p v-if="upstreams.loading" class="mt-4 text-sm text-slate-500">Loading…</p>
    <p v-else-if="upstreams.error" class="mt-4 text-sm text-rose-600">{{ upstreams.error }}</p>
    <p v-else-if="upstreams.items.length === 0" class="mt-4 text-sm text-slate-500">
      No upstreams configured for this tenant.
    </p>
    <ul v-else class="mt-4 space-y-2">
      <li
        v-for="up in upstreams.items"
        :key="up.publicId"
        class="rounded-md border border-slate-200 bg-white p-3 dark:border-slate-700 dark:bg-slate-800"
      >
        <div class="flex items-center justify-between">
          <div>
            <p class="font-medium">{{ up.displayName || up.name }}</p>
            <p class="text-xs text-slate-500">{{ up.strategyType }} · {{ up.linkState }}</p>
          </div>
        </div>
      </li>
    </ul>
  </section>
</template>
