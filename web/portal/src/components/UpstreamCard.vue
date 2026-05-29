<script setup lang="ts">
import { computed } from 'vue'
import type { UpstreamSummary } from '@gen/limen/portal/v1/portal_pb.js'
import {
  upstreamCTAs,
  linkStateLabel,
  linkStateTone,
  staticHeaderModeLabel,
  type CTAKind,
} from '@limen/shared'
import UpstreamCatalog from '@/components/UpstreamCatalog.vue'

const props = defineProps<{
  upstream: UpstreamSummary
  busy?: boolean
}>()

const emit = defineEmits<{
  (e: 'action', kind: CTAKind): void
}>()

const ctas = computed(() => upstreamCTAs(props.upstream))
const stateLabel = computed(() => linkStateLabel(props.upstream.linkState))
const stateTone = computed(() => linkStateTone(props.upstream.linkState))

const subModeSuffix = computed(() => {
  if (!props.upstream.strategySubMode) return ''
  return ` · ${staticHeaderModeLabel(props.upstream.strategySubMode)}`
})

function variantClass(variant: 'primary' | 'secondary' | 'danger'): string {
  switch (variant) {
    case 'primary':
      return 'bg-indigo-600 text-white hover:bg-indigo-500 disabled:opacity-50'
    case 'danger':
      return 'bg-rose-600 text-white hover:bg-rose-500 disabled:opacity-50'
    case 'secondary':
    default:
      return 'bg-slate-100 text-slate-800 hover:bg-slate-200 dark:bg-slate-700 dark:text-slate-100 dark:hover:bg-slate-600 disabled:opacity-50'
  }
}
</script>

<template>
  <article
    class="rounded-md border border-slate-200 bg-white p-4 dark:border-slate-700 dark:bg-slate-800"
    :data-upstream-name="upstream.identifier"
  >
    <header class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <h2 class="truncate text-base font-medium">
          {{ upstream.displayName || upstream.identifier }}
        </h2>
        <p class="mt-0.5 text-xs text-slate-500">
          {{ upstream.strategyType }}{{ subModeSuffix }} · {{ upstream.mcpUrl }}
        </p>
      </div>
      <span
        class="shrink-0 rounded-full px-2 py-0.5 text-xs font-medium"
        :class="stateTone"
        :data-link-state="stateLabel"
      >
        {{ stateLabel }}
      </span>
    </header>

    <p v-if="upstream.lastErrorReason" class="mt-2 text-xs text-amber-700 dark:text-amber-300">
      Last failure: {{ upstream.lastErrorReason }}
      <span v-if="upstream.lastErrorAt"> · {{ upstream.lastErrorAt }}</span>
    </p>

    <p v-if="ctas.length === 0" class="mt-3 text-xs text-slate-500">
      Tools from this upstream are available to all members of the tenant — no action needed.
    </p>

    <div v-else class="mt-3 flex flex-wrap gap-2">
      <button
        v-for="cta in ctas"
        :key="cta.kind"
        type="button"
        :disabled="busy"
        :class="['rounded-md px-3 py-1.5 text-sm font-medium', variantClass(cta.variant)]"
        :data-cta="cta.kind"
        @click="emit('action', cta.kind)"
      >
        {{ cta.label }}
      </button>
    </div>

    <UpstreamCatalog :upstream="upstream" />
  </article>
</template>
