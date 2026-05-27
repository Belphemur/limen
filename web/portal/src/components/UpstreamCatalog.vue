<script setup lang="ts">
import { computed } from 'vue'
import type { UpstreamSummary, UpstreamTool } from '@gen/limen/portal/v1/portal_pb.js'

const props = defineProps<{
  upstream: UpstreamSummary
}>()

const tools = computed<UpstreamTool[]>(() => props.upstream.tools ?? [])
const aliases = computed<string[]>(() => props.upstream.aliases ?? [])
const summary = computed(() => {
  const t = tools.value.length
  const a = aliases.value.length
  const parts: string[] = []
  parts.push(t === 1 ? '1 tool' : `${t} tools`)
  if (a > 0) parts.push(a === 1 ? '1 alias' : `${a} aliases`)
  return parts.join(' · ')
})
</script>

<template>
  <details
    v-if="tools.length > 0 || aliases.length > 0"
    class="mt-3 rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-sm dark:border-slate-700 dark:bg-slate-900"
    :data-upstream-catalog="upstream.identifier"
  >
    <summary class="cursor-pointer select-none text-slate-600 dark:text-slate-300">
      {{ summary }}
    </summary>
    <div v-if="aliases.length > 0" class="mt-2">
      <p class="text-xs font-medium uppercase tracking-wide text-slate-500">Aliases</p>
      <ul class="mt-1 flex flex-wrap gap-1">
        <li
          v-for="alias in aliases"
          :key="alias"
          class="rounded bg-indigo-100 px-1.5 py-0.5 font-mono text-xs text-indigo-800 dark:bg-indigo-900/40 dark:text-indigo-200"
          data-alias
        >
          {{ alias }}
        </li>
      </ul>
    </div>
    <div v-if="tools.length > 0" class="mt-2">
      <p class="text-xs font-medium uppercase tracking-wide text-slate-500">Tools</p>
      <ul class="mt-1 grid gap-1">
        <li
          v-for="tool in tools"
          :key="tool.name"
          class="rounded border border-slate-200 bg-white px-2 py-1 dark:border-slate-700 dark:bg-slate-800"
          data-tool
        >
          <p class="font-mono text-xs text-slate-800 dark:text-slate-100">{{ tool.name }}</p>
          <p
            v-if="tool.description"
            class="mt-0.5 line-clamp-2 text-xs text-slate-500"
            :title="tool.description"
          >
            {{ tool.description }}
          </p>
        </li>
      </ul>
    </div>
  </details>
</template>
