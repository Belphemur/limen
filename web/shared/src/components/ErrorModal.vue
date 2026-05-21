<script setup lang="ts">
import { AlertCircle } from "@lucide/vue";

defineProps<{
  open: boolean;
  title: string;
  message: string;
  primaryLabel?: string;
  secondaryLabel?: string;
}>();

const emit = defineEmits<{
  (e: "primary"): void;
  (e: "secondary"): void;
  (e: "close"): void;
}>();

function onBackdrop(ev: MouseEvent) {
  if (ev.target === ev.currentTarget) emit("close");
}
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition-opacity duration-150"
      enter-from-class="opacity-0"
      enter-to-class="opacity-100"
      leave-active-class="transition-opacity duration-100"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
    >
      <div
        v-if="open"
        class="fixed inset-0 z-50 flex items-center justify-center bg-on-background/40 p-4 backdrop-blur-sm"
        role="dialog"
        aria-modal="true"
        data-testid="error-modal"
        @click="onBackdrop"
      >
        <div
          class="flex w-full max-w-md transform flex-col overflow-hidden rounded-xl border border-border-light bg-surface-container-highest shadow-[0_20px_25px_-5px_rgba(0,0,0,0.1)] transition-all"
        >
          <div class="flex flex-col items-center p-6 text-center">
            <div
              class="mb-stack-md flex h-16 w-16 shrink-0 items-center justify-center rounded-full bg-error-container"
            >
              <AlertCircle :size="32" class="text-error" aria-hidden="true" />
            </div>
            <h2
              class="mb-stack-sm font-headline-lg text-headline-lg text-on-surface"
            >
              {{ title }}
            </h2>
            <p
              class="mb-stack-lg font-body-md text-body-md text-on-surface-variant"
            >
              {{ message }}
            </p>
            <div class="mt-4 flex w-full flex-col gap-3">
              <button
                v-if="primaryLabel"
                type="button"
                class="w-full rounded-lg bg-primary-container px-4 py-2 font-label-md text-label-md text-on-primary shadow-sm transition-colors hover:bg-primary"
                data-testid="error-modal-primary"
                @click="emit('primary')"
              >
                {{ primaryLabel }}
              </button>
              <button
                type="button"
                class="w-full rounded-lg border border-border-light bg-surface-container-lowest px-4 py-2 font-label-md text-label-md text-on-surface transition-colors hover:bg-surface-container-low"
                data-testid="error-modal-secondary"
                @click="emit('secondary')"
              >
                {{ secondaryLabel ?? "Cancel" }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
