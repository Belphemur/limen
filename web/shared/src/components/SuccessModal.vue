<script setup lang="ts">
import { CheckCircle2, Server } from "@lucide/vue";

defineProps<{
  open: boolean;
  title: string;
  message: string;
  // Optional small chip below the headline (e.g. the upstream identifier).
  chip?: string;
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
      enter-active-class="transition-opacity duration-200"
      enter-from-class="opacity-0"
      enter-to-class="opacity-100"
      leave-active-class="transition-opacity duration-150"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
    >
      <div
        v-if="open"
        class="fixed inset-0 z-50 flex items-center justify-center bg-on-background/40 p-4 backdrop-blur-sm"
        role="dialog"
        aria-modal="true"
        data-testid="success-modal"
        @click="onBackdrop"
      >
        <div
          class="relative flex w-full max-w-[540px] flex-col items-center rounded-xl border border-border-light bg-surface p-10 text-center shadow-[0_4px_20px_-4px_rgba(0,0,0,0.05),0_0_2px_rgba(0,0,0,0.08)]"
        >
          <div class="relative mb-6">
            <div
              class="absolute inset-0 animate-ping rounded-full bg-success/10"
              style="animation-duration: 2s"
            ></div>
            <div
              class="relative z-10 flex h-24 w-24 items-center justify-center rounded-full bg-success/20"
            >
              <CheckCircle2
                :size="56"
                class="text-success"
                aria-hidden="true"
              />
            </div>
          </div>
          <h2 class="mb-2 font-display text-display text-on-surface">
            {{ title }}
          </h2>
          <div
            v-if="chip"
            class="mb-6 inline-flex items-center gap-2 rounded-full border border-surface-dim bg-surface-container-low px-4 py-1.5"
          >
            <Server :size="18" class="text-primary" aria-hidden="true" />
            <span
              class="font-data-mono text-data-mono font-medium tracking-wide text-primary"
            >
              {{ chip }}
            </span>
          </div>
          <p
            class="mb-10 max-w-[400px] font-body-lg text-body-lg text-on-surface-variant"
          >
            {{ message }}
          </p>
          <div class="flex w-full flex-col gap-4 sm:flex-row">
            <button
              v-if="secondaryLabel"
              type="button"
              class="flex-1 rounded-lg border border-border-light bg-surface px-6 py-3 font-label-md text-label-md text-on-surface shadow-sm transition-colors hover:bg-surface-container-high focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2"
              data-testid="success-modal-secondary"
              @click="emit('secondary')"
            >
              {{ secondaryLabel }}
            </button>
            <button
              v-if="primaryLabel"
              type="button"
              class="flex-1 rounded-lg bg-primary px-6 py-3 font-label-md text-label-md text-white shadow-[0_2px_4px_rgba(60,80,224,0.2)] transition-colors hover:bg-primary-container focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2"
              data-testid="success-modal-primary"
              @click="emit('primary')"
            >
              {{ primaryLabel }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
