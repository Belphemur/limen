<script setup lang="ts">
import { Monitor, Sun, Moon } from '@lucide/vue'
import { useThemeStore, type ThemeMode } from '@/stores/theme'

const theme = useThemeStore()

const options: { value: ThemeMode; label: string; icon: typeof Monitor }[] = [
  { value: 'system', label: 'System', icon: Monitor },
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'dark', label: 'Dark', icon: Moon },
]

function onKey(event: KeyboardEvent, index: number) {
  if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return
  event.preventDefault()
  const dir = event.key === 'ArrowRight' ? 1 : -1
  const next = (index + dir + options.length) % options.length
  theme.set(options[next].value)
  const buttons = (event.currentTarget as HTMLElement).parentElement?.querySelectorAll('button')
  buttons?.[next]?.focus()
}
</script>

<template>
  <div
    role="radiogroup"
    aria-label="Theme"
    class="inline-flex items-center gap-1 rounded-lg border border-border-subtle bg-surface p-1"
  >
    <button
      v-for="(opt, index) in options"
      :key="opt.value"
      type="button"
      role="radio"
      :aria-checked="theme.mode === opt.value"
      :aria-label="opt.label"
      :title="opt.label"
      class="inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded text-on-surface-variant transition-colors hover:text-on-surface focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/30"
      :class="{
        'bg-primary text-on-primary hover:text-on-primary': theme.mode === opt.value,
      }"
      :tabindex="theme.mode === opt.value ? 0 : -1"
      @click="theme.set(opt.value)"
      @keydown="onKey($event, index)"
    >
      <component :is="opt.icon" :size="16" aria-hidden="true" />
    </button>
  </div>
</template>
