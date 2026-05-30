<script setup lang="ts">
import { Eye, EyeOff } from "@lucide/vue";
import { nextTick, ref, watch } from "vue";
import BaseModal from "./BaseModal.vue";

const props = defineProps<{
    open: boolean;
    title: string;
    description: string;
    label: string;
    busy?: boolean;
}>();

const emit = defineEmits<{
    (e: "confirm", secret: string): void;
    (e: "cancel"): void;
}>();

const secret = ref("");
const showSecret = ref(false);
const inputEl = ref<HTMLInputElement | null>(null);

watch(
    () => props.open,
    (isOpen) => {
        if (isOpen) {
            secret.value = "";
            showSecret.value = false;
            nextTick(() => {
                inputEl.value?.focus();
            });
        }
    },
    { immediate: true },
);

function onConfirm() {
    if (!secret.value || props.busy) return;
    emit("confirm", secret.value);
}
</script>

<template>
    <BaseModal :open="open" :dismissible="!busy" @close="emit('cancel')">
        <div
            class="flex w-full max-w-md transform flex-col overflow-hidden rounded-xl border border-border-subtle bg-surface-container-highest shadow-[0_20px_25px_-5px_rgba(0,0,0,0.1)] transition-all mx-4"
            role="dialog"
            aria-modal="true"
            aria-labelledby="secret-modal-title"
            @click.stop
        >
            <div class="flex flex-col p-6">
                <h2 id="secret-modal-title" class="mb-stack-sm font-headline-lg text-headline-lg text-on-surface">
                    {{ title }}
                </h2>
                <p class="mb-stack-md font-body-md text-body-md text-on-surface-variant">
                    {{ description }}
                </p>

                <form @submit.prevent="onConfirm">
                    <label class="block font-body-md text-body-md text-on-surface">
                        {{ label }}
                    </label>
                    <div class="relative mt-1">
                        <input
                            ref="inputEl"
                            v-model="secret"
                            :type="showSecret ? 'text' : 'password'"
                            autocomplete="off"
                            :disabled="busy"
                            class="block w-full rounded-lg border border-outline-variant bg-surface-container-lowest px-3 py-2 pr-10 text-sm text-on-surface placeholder:text-on-surface-variant focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary disabled:opacity-50"
                            data-testid="secret-input"
                        />
                        <button
                            type="button"
                            class="absolute inset-y-0 right-0 flex items-center pr-3 text-on-surface-variant hover:text-on-surface"
                            :aria-label="showSecret ? 'Hide secret' : 'Show secret'"
                            @click="showSecret = !showSecret"
                        >
                            <Eye v-if="showSecret" :size="16" />
                            <EyeOff v-else :size="16" />
                        </button>
                    </div>

                    <div class="mt-stack-lg flex w-full flex-col gap-3">
                        <button
                            type="submit"
                            :disabled="busy || !secret"
                            class="inline-flex w-full items-center justify-center gap-1.5 rounded-lg bg-primary px-4 py-2 font-label-md text-label-md text-on-primary shadow-sm transition-colors hover:bg-primary/90 disabled:opacity-50"
                            data-testid="secret-confirm"
                        >
                            {{ busy ? 'Working…' : 'Confirm' }}
                        </button>
                        <button
                            type="button"
                            :disabled="busy"
                            class="w-full rounded-lg border border-border-subtle bg-surface-container-lowest px-4 py-2 font-label-md text-label-md text-on-surface transition-colors hover:bg-surface-container-low disabled:opacity-50"
                            data-testid="secret-cancel"
                            @click="emit('cancel')"
                        >
                            Cancel
                        </button>
                    </div>
                </form>
            </div>
        </div>
    </BaseModal>
</template>
