<script setup lang="ts">
// Reusable v-model[string[]] editor for DCR redirect-URI allowlist
// patterns. Each row validates against
// validateRedirectURIPattern from ../lib/redirectURI.ts so the admin
// gets per-row feedback as they type. Server-side validation in
// internal/oauthproxy.ValidateRedirectURIPattern remains
// authoritative.

import { computed, ref, watch } from 'vue'
import { Plus, Trash2 } from '@lucide/vue'
import {
    validateRedirectURIPattern,
    type RedirectURIValidation,
} from '../lib/redirectURI'

interface Props {
    modelValue: string[]
    disabled?: boolean
}

const props = withDefaults(defineProps<Props>(), { disabled: false })
const emit = defineEmits<{
    'update:modelValue': [value: string[]]
    'validity-change': [allValid: boolean]
}>()

// Local editable buffer; we sync to/from props but keep the editor
// responsive without forcing the parent to recompute on every keystroke.
const entries = ref<string[]>([...props.modelValue])

watch(
    () => props.modelValue,
    (next) => {
        if (next.length !== entries.value.length || next.some((v, i) => v !== entries.value[i])) {
            entries.value = [...next]
        }
    },
)

const validations = computed<RedirectURIValidation[]>(() =>
    entries.value.map((e) => (e.trim() === '' ? { ok: false, reason: 'pattern is empty' } : validateRedirectURIPattern(e.trim()))),
)

const allValid = computed(() => validations.value.every((v) => v.ok))

watch(allValid, (v) => emit('validity-change', v), { immediate: true })

function emitUpdate() {
    emit('update:modelValue', entries.value.map((e) => e.trim()))
}

function onInput(index: number, ev: Event) {
    entries.value[index] = (ev.target as HTMLInputElement).value
    emitUpdate()
}

function addEntry() {
    entries.value.push('')
    emitUpdate()
}

function removeEntry(index: number) {
    entries.value.splice(index, 1)
    emitUpdate()
}
</script>

<template>
    <div class="space-y-2">
        <ul v-if="entries.length > 0" class="space-y-2" data-testid="allowlist-list">
            <li v-for="(entry, index) in entries" :key="index" class="flex items-start gap-2"
                :data-testid="`allowlist-row-${index}`">
                <div class="flex-1">
                    <input type="text" :value="entry" :disabled="disabled" placeholder="https://*.example.com/cb"
                        spellcheck="false" autocomplete="off"
                        class="w-full rounded border border-outline bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none"
                        :class="{
                            'border-error': !validations[index].ok && entry !== '',
                        }" :aria-invalid="!validations[index].ok && entry !== ''" :aria-describedby="`allowlist-row-${index}-err`"
                        @input="onInput(index, $event)" />
                    <p v-if="!validations[index].ok && entry !== ''" :id="`allowlist-row-${index}-err`"
                        class="mt-1 text-xs text-error">
                        {{ (validations[index] as { ok: false; reason: string }).reason }}
                    </p>
                </div>
                <button type="button" :disabled="disabled"
                    class="rounded p-2 text-on-surface-variant hover:bg-surface-variant focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
                    :aria-label="`Remove entry ${index + 1}`" @click="removeEntry(index)">
                    <Trash2 class="h-4 w-4" />
                </button>
            </li>
        </ul>
        <p v-else class="text-sm text-on-surface-variant">
            No allowlist entries — only the global HTTPS / loopback floor will be applied.
        </p>
        <button type="button" :disabled="disabled"
            class="inline-flex items-center gap-1 rounded border border-outline px-3 py-1.5 text-sm text-on-surface hover:bg-surface-variant focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
            data-testid="allowlist-add" @click="addEntry">
            <Plus class="h-4 w-4" />
            Add entry
        </button>
    </div>
</template>
