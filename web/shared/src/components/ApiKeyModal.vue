<script setup lang="ts">
import { ref, watch } from 'vue'
import { Loader2 } from '@lucide/vue'
import BaseModal from './BaseModal.vue'

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
  <BaseModal :open="open" @close="emit('cancel')">
    <div
      class="w-full max-w-md rounded-lg bg-surface p-5 shadow-xl"
      role="dialog"
      aria-modal="true"
      data-modal="api-key"
      @click.stop
    >
      <h2 class="font-display text-lg font-semibold text-on-surface">
        {{ title || `API key for ${upstreamLabel}` }}
      </h2>
      <p class="mt-1 text-sm text-on-surface-variant">
        Limen encrypts this value and never logs it. Rotating overwrites the previous key.
      </p>
      <form class="mt-4 space-y-3" @submit.prevent="onSubmit">
        <label class="block text-sm">
          <span class="font-medium text-on-surface">API key</span>
          <input
            v-model="apiKey"
            type="password"
            autocomplete="off"
            required
            class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface placeholder:text-on-surface-variant focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            data-input="api-key"
          />
        </label>
        <div class="flex justify-end gap-2 pt-2">
          <button
            type="button"
            class="rounded-md px-3 py-1.5 text-sm font-medium text-on-surface-variant hover:bg-surface-container-low"
            :disabled="busy"
            @click="emit('cancel')"
          >
            Cancel
          </button>
          <button
            type="submit"
            class="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm font-medium text-on-primary shadow-sm hover:bg-primary/90 disabled:opacity-50"
            :disabled="busy || !apiKey"
            data-cta="submit-api-key"
          >
            <Loader2 v-if="busy" :size="14" class="animate-spin" />
            Save
          </button>
        </div>
      </form>
    </div>
  </BaseModal>
</template>
