<script setup lang="ts">
import { nextTick, ref, watch } from "vue";
import { AlertCircle, Trash2 } from "@lucide/vue";
import BaseModal from "./BaseModal.vue";

const props = defineProps<{
    open: boolean;
    title: string;
    message: string;
    /** Token the user must type verbatim to enable the destructive action. */
    confirmToken: string;
    confirmLabel?: string;
    cancelLabel?: string;
    busy?: boolean;
}>();

const emit = defineEmits<{
    (e: "confirm"): void;
    (e: "cancel"): void;
}>();

const input = ref("");
const inputEl = ref<HTMLInputElement | null>(null);

watch(
    () => props.open,
    (open) => {
        if (open) {
            input.value = "";
            nextTick(() => inputEl.value?.focus());
        }
    },
);

function onConfirm() {
    if (input.value === props.confirmToken && !props.busy) emit("confirm");
}
</script>

<template>
    <BaseModal :open="open" :dismissible="!busy" testid="confirm-delete-modal" @close="emit('cancel')">
        <div
            class="flex w-full max-w-md transform flex-col overflow-hidden rounded-xl border border-error/40 bg-surface-container-highest shadow-[0_20px_25px_-5px_rgba(0,0,0,0.1)] transition-all">
            <div class="flex flex-col items-center p-6 text-center">
                <div
                    class="mb-stack-md flex h-16 w-16 shrink-0 items-center justify-center rounded-full bg-error-container">
                    <AlertCircle :size="32" class="text-error" aria-hidden="true" />
                </div>
                <h2 class="mb-stack-sm font-headline-lg text-headline-lg text-on-surface">
                    {{ title }}
                </h2>
                <p class="mb-stack-md font-body-md text-body-md text-on-surface-variant">
                    {{ message }}
                </p>
                <p class="mb-stack-sm w-full text-left text-sm text-on-surface-variant">
                    Type
                    <code class="font-mono text-on-surface">{{ confirmToken }}</code>
                    to confirm.
                </p>
                <input ref="inputEl" v-model="input" type="text" :disabled="busy"
                    class="block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-error focus:outline-none focus:ring-1 focus:ring-error disabled:opacity-50"
                    data-testid="confirm-delete-input" @keydown.enter="onConfirm" />
                <div class="mt-4 flex w-full flex-col gap-3">
                    <button type="button" :disabled="busy || input !== confirmToken"
                        class="inline-flex w-full items-center justify-center gap-1.5 rounded-lg bg-error px-4 py-2 font-label-md text-label-md text-on-error shadow-sm transition-colors hover:bg-error/90 disabled:opacity-50"
                        data-testid="confirm-delete-confirm" @click="onConfirm">
                        <Trash2 :size="16" aria-hidden="true" />
                        {{ busy ? "Deleting…" : (confirmLabel ?? "Delete") }}
                    </button>
                    <button type="button" :disabled="busy"
                        class="w-full rounded-lg border border-border-light bg-surface-container-lowest px-4 py-2 font-label-md text-label-md text-on-surface transition-colors hover:bg-surface-container-low disabled:opacity-50"
                        data-testid="confirm-delete-cancel" @click="emit('cancel')">
                        {{ cancelLabel ?? "Cancel" }}
                    </button>
                </div>
            </div>
        </div>
    </BaseModal>
</template>
