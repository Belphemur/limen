<script setup lang="ts">
import { ref, watch } from 'vue'

const props = defineProps<{
  open: boolean
  upstreamLabel: string
  title?: string
  busy?: boolean
}>()

const emit = defineEmits<{
  (e: 'submit', apiKey: string): void
  (e: 'cancel'): void
}>()

const apiKey = ref('')

// Clear the field whenever the modal closes — never keep a previous
// rotation's secret in memory longer than necessary.
watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) apiKey.value = ''
  },
)

function onSubmit() {
  if (!apiKey.value) return
  emit('submit', apiKey.value)
}
</script>

<template>
  <div
    v-if="open"
    class="fixed inset-0 z-40 flex items-center justify-center bg-slate-900/50"
    role="dialog"
    aria-modal="true"
    data-modal="api-key"
  >
    <div class="w-full max-w-md rounded-md bg-white p-5 shadow-lg dark:bg-slate-800" @click.stop>
      <h2 class="text-lg font-semibold">
        {{ title || `API key for ${upstreamLabel}` }}
      </h2>
      <p class="mt-1 text-sm text-slate-500">
        Limen encrypts this value with a per-user AAD and never logs it. Rotating overwrites the
        previous key.
      </p>
      <form class="mt-4 space-y-3" @submit.prevent="onSubmit">
        <label class="block text-sm">
          <span class="text-slate-700 dark:text-slate-200">API key</span>
          <input
            v-model="apiKey"
            type="password"
            autocomplete="off"
            required
            class="mt-1 block w-full rounded-md border border-slate-300 px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900"
            data-input="api-key"
          />
        </label>
        <div class="flex justify-end gap-2 pt-2">
          <button
            type="button"
            class="rounded-md px-3 py-1.5 text-sm font-medium text-slate-700 hover:bg-slate-100 dark:text-slate-200 dark:hover:bg-slate-700"
            :disabled="busy"
            @click="emit('cancel')"
          >
            Cancel
          </button>
          <button
            type="submit"
            class="rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-500 disabled:opacity-50"
            :disabled="busy || !apiKey"
            data-cta="submit-api-key"
          >
            Save
          </button>
        </div>
      </form>
    </div>
  </div>
</template>
