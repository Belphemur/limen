<script setup lang="ts">
import { AlertTriangle } from '@lucide/vue'
import BaseModal from './BaseModal.vue'

const props = withDefaults(defineProps<{
    open: boolean
    title: string
    message: string
    primaryLabel?: string
    cancelLabel?: string
    busy?: boolean
}>(), {
    primaryLabel: 'Confirm',
    cancelLabel: 'Cancel',
    busy: false,
})

const emit = defineEmits<{
    confirm: []
    cancel: []
}>()
</script>

<template>
    <BaseModal :open="open" :dismissible="!busy" testid="confirm-action-modal" @close="emit('cancel')">
        <div
            class="flex w-full max-w-md transform flex-col overflow-hidden rounded-xl border border-error/40 bg-surface-container-highest shadow-[0_20px_25px_-5px_rgba(0,0,0,0.1)] transition-all">
            <div class="flex flex-col items-center p-6 text-center">
                <div
                    class="mb-stack-md flex h-16 w-16 shrink-0 items-center justify-center rounded-full bg-error-container">
                    <AlertTriangle :size="32" class="text-error" aria-hidden="true" />
                </div>
                <h2 class="mb-stack-sm font-headline-lg text-headline-lg text-on-surface">
                    {{ title }}
                </h2>
                <p class="mb-stack-md font-body-md text-body-md text-on-surface-variant">
                    {{ message }}
                </p>
                <div class="flex w-full flex-col gap-3">
                    <button type="button" :disabled="busy"
                        class="inline-flex w-full items-center justify-center gap-1.5 rounded-lg bg-error px-4 py-2 font-label-md text-label-md text-on-error shadow-sm transition-colors hover:bg-error/90 disabled:opacity-50"
                        data-testid="confirm-action-confirm" @click="emit('confirm')">
                        {{ props.busy ? 'Working…' : props.primaryLabel }}
                    </button>
                    <button type="button" :disabled="busy"
                        class="w-full rounded-lg border border-border-light bg-surface-container-lowest px-4 py-2 font-label-md text-label-md text-on-surface transition-colors hover:bg-surface-container-low disabled:opacity-50"
                        data-testid="confirm-action-cancel" @click="emit('cancel')">
                        {{ props.cancelLabel }}
                    </button>
                </div>
            </div>
        </div>
    </BaseModal>
</template>
