<script setup lang="ts">
defineProps<{
  completed: number
  total: number
}>()
</script>

<script lang="ts">
export function progressPercent(completed: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((completed / total) * 100)))
}
</script>

<template>
  <section
    class="rounded-lg border border-border-subtle bg-surface p-6 shadow-sm"
    aria-labelledby="setup-progress-title"
  >
    <div class="flex items-start justify-between gap-4">
      <div>
        <h2 id="setup-progress-title" class="font-display text-lg font-semibold text-on-surface">
          Setup Progress
        </h2>
        <p class="mt-1 text-sm text-on-surface-variant">
          {{ completed }} of {{ total }} steps completed
        </p>
      </div>
      <p
        class="font-display text-3xl font-semibold text-primary tabular-nums"
        :aria-label="`${progressPercent(completed, total)} percent`"
      >
        {{ progressPercent(completed, total) }}%
      </p>
    </div>
    <div
      class="mt-4 h-2 w-full overflow-hidden rounded-full bg-border-subtle"
      role="progressbar"
      :aria-valuenow="progressPercent(completed, total)"
      aria-valuemin="0"
      aria-valuemax="100"
    >
      <div
        class="h-full rounded-full bg-primary transition-all duration-300"
        :style="{ width: `${progressPercent(completed, total)}%` }"
      />
    </div>
  </section>
</template>
