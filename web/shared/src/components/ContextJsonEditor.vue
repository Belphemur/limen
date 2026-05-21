<script setup lang="ts">
import { computed, watch } from 'vue'

const props = defineProps<{
  modelValue: string
  // Max size accepted by the backend (internal/contextblob).
  maxBytes?: number
  // Rows for the textarea.
  rows?: number
  // Placeholder JSON shown when empty.
  placeholder?: string
  // Optional caption rendered under the editor.
  caption?: string
  // Disable editing.
  disabled?: boolean
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'update:valid', valid: boolean): void
}>()

const MAX_BYTES_DEFAULT = 4096
const KEY_RE = /^[A-Za-z_$][\w$]*$/

interface ParseState {
  ok: boolean
  byteLength: number
  message: string
}

const state = computed<ParseState>(() => {
  const raw = props.modelValue ?? ''
  const max = props.maxBytes ?? MAX_BYTES_DEFAULT
  const byteLength = new TextEncoder().encode(raw).length
  if (raw.trim() === '') {
    return { ok: true, byteLength, message: 'Empty (will be treated as `{}`)' }
  }
  if (byteLength > max) {
    return { ok: false, byteLength, message: `Too large (${byteLength} > ${max} B)` }
  }
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch (err) {
    return {
      ok: false,
      byteLength,
      message: `Invalid JSON: ${(err as Error).message}`,
    }
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { ok: false, byteLength, message: 'Must be a JSON object (e.g. `{}`).' }
  }
  for (const key of Object.keys(parsed as Record<string, unknown>)) {
    if (!KEY_RE.test(key)) {
      return {
        ok: false,
        byteLength,
        message: `Invalid key "${key}" — keys must match /^[A-Za-z_$][\\w$]*$/.`,
      }
    }
  }
  return { ok: true, byteLength, message: `Valid · ${byteLength} B` }
})

watch(
  () => state.value.ok,
  (ok) => emit('update:valid', ok),
  { immediate: true },
)

function onInput(e: Event) {
  emit('update:modelValue', (e.target as HTMLTextAreaElement).value)
}
</script>

<template>
  <div class="space-y-1">
    <textarea
      :value="modelValue"
      :rows="rows ?? 6"
      :placeholder="placeholder ?? '{\n  &quot;cloudId&quot;: &quot;&quot;\n}'"
      :disabled="disabled"
      spellcheck="false"
      class="block w-full rounded-md border bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
      :class="state.ok ? 'border-outline-variant' : 'border-error'"
      data-testid="context-json-editor"
      @input="onInput"
    />
    <p
      class="text-xs"
      :class="state.ok ? 'text-on-surface-variant' : 'text-error'"
      data-testid="context-json-status"
    >
      {{ state.message }}
    </p>
    <p v-if="caption" class="text-xs text-on-surface-variant">{{ caption }}</p>
  </div>
</template>
