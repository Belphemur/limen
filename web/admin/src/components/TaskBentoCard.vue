<script setup lang="ts">
import { Check } from '@lucide/vue'
import type { Component } from 'vue'

defineProps<{
  variant: 'primary' | 'secondary'
  icon: Component
  title: string
  body: string
  ctaLabel: string
  ctaIcon?: Component
  done: boolean
}>()

defineEmits<{
  (e: 'activate'): void
}>()
</script>

<template>
  <article
    class="relative flex flex-col rounded-lg border border-border-subtle p-6 shadow-sm transition-colors"
    :class="done ? 'bg-surface-container-low' : variant === 'primary' ? 'bg-surface' : 'bg-surface'"
  >
    <span
      v-if="done"
      aria-label="Completed"
      class="absolute right-4 top-4 inline-flex h-6 w-6 items-center justify-center rounded-full bg-success/10 text-success"
    >
      <Check :size="14" aria-hidden="true" />
    </span>

    <div
      class="mb-4 inline-flex h-12 w-12 items-center justify-center rounded-lg"
      :class="
        variant === 'primary'
          ? 'bg-primary/10 text-primary'
          : 'bg-surface-container text-on-surface-variant'
      "
    >
      <component :is="icon" :size="24" aria-hidden="true" />
    </div>

    <h3 class="font-display text-base font-semibold text-on-surface">{{ title }}</h3>
    <p class="mt-1 flex-1 text-sm text-on-surface-variant">{{ body }}</p>

    <button
      type="button"
      class="mt-4 inline-flex items-center gap-1.5 self-start rounded px-3 py-2 text-sm font-medium transition-colors"
      :class="
        done
          ? 'text-on-surface-variant hover:bg-surface-container-low hover:text-on-surface'
          : variant === 'primary'
            ? 'bg-primary text-on-primary shadow-sm hover:bg-primary/90'
            : 'border border-border-subtle bg-surface text-on-surface hover:bg-surface-container-low'
      "
      @click="$emit('activate')"
    >
      {{ ctaLabel }}
      <component :is="ctaIcon" v-if="ctaIcon" :size="16" aria-hidden="true" />
    </button>
  </article>
</template>
