<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";

const props = defineProps<{
    text: string;
}>();

const visible = ref(false);
const triggerRef = ref<HTMLElement | null>(null);
const tooltipRef = ref<HTMLElement | null>(null);
const position = ref<"above" | "below">("below");
const coords = ref({ left: 0, top: 0 });

let showTimeout: ReturnType<typeof setTimeout> | null = null;

function show() {
    if (showTimeout) clearTimeout(showTimeout);
    showTimeout = setTimeout(() => {
        visible.value = true;
        nextTick(updatePosition);
    }, 150);
}

function hide() {
    if (showTimeout) {
        clearTimeout(showTimeout);
        showTimeout = null;
    }
    visible.value = false;
}

function updatePosition() {
    const trigger = triggerRef.value;
    const tooltip = tooltipRef.value;
    if (!trigger || !tooltip) return;

    const triggerRect = trigger.getBoundingClientRect();
    const tooltipRect = tooltip.getBoundingClientRect();
    const margin = 8;
    const arrowHeight = 4;

    const spaceBelow = window.innerHeight - triggerRect.bottom;
    const fitsBelow = spaceBelow >= tooltipRect.height + margin + arrowHeight;

    if (fitsBelow) {
        position.value = "below";
        coords.value.top = triggerRect.bottom + margin + arrowHeight;
    } else {
        position.value = "above";
        coords.value.top = triggerRect.top - tooltipRect.height - margin - arrowHeight;
    }

    const left = triggerRect.left + triggerRect.width / 2 - tooltipRect.width / 2;
    const clampedLeft = Math.max(margin, Math.min(left, window.innerWidth - tooltipRect.width - margin));
    coords.value.left = clampedLeft;
}

watch(visible, (isVisible) => {
    if (isVisible) {
        window.addEventListener("scroll", updatePosition, true);
        window.addEventListener("resize", updatePosition);
    } else {
        window.removeEventListener("scroll", updatePosition, true);
        window.removeEventListener("resize", updatePosition);
    }
});

onBeforeUnmount(() => {
    if (showTimeout) clearTimeout(showTimeout);
    window.removeEventListener("scroll", updatePosition, true);
    window.removeEventListener("resize", updatePosition);
});
</script>

<template>
    <div ref="triggerRef" class="inline-flex" @mouseenter="show" @mouseleave="hide" @focusin="show" @focusout="hide">
        <slot />
    </div>

    <Teleport to="body">
        <Transition enter-active-class="transition-opacity duration-300" enter-from-class="opacity-0"
            enter-to-class="opacity-100" leave-active-class="transition-opacity duration-100" leave-from-class="opacity-100"
            leave-to-class="opacity-0">
            <div v-if="visible" ref="tooltipRef"
                class="fixed z-50 max-w-xs break-words rounded-lg bg-inverse-surface px-3 py-2 text-xs font-medium text-inverse-on-surface shadow-sm"
                role="tooltip" :style="{ left: `${coords.left}px`, top: `${coords.top}px` }">
                {{ text }}
                <span v-if="position === 'below'"
                    class="absolute left-1/2 top-0 h-2 w-2 -translate-x-1/2 -translate-y-1/2 rotate-45 bg-inverse-surface" />
                <span v-else class="absolute bottom-0 left-1/2 h-2 w-2 -translate-x-1/2 translate-y-1/2 rotate-45 bg-inverse-surface" />
            </div>
        </Transition>
    </Teleport>
</template>
