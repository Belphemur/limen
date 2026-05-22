<script setup lang="ts">
import { onBeforeUnmount, watch } from "vue";

const props = withDefaults(
    defineProps<{
        open: boolean;
        /** When false, backdrop click and Escape no longer emit `close`. */
        dismissible?: boolean;
        /** Optional data-testid for the backdrop element. */
        testid?: string;
    }>(),
    { dismissible: true },
);

const emit = defineEmits<{ (e: "close"): void }>();

function onKeydown(ev: KeyboardEvent) {
    if (ev.key === "Escape" && props.open && props.dismissible) {
        ev.preventDefault();
        emit("close");
    }
}

watch(
    () => props.open,
    (open) => {
        if (open) window.addEventListener("keydown", onKeydown);
        else window.removeEventListener("keydown", onKeydown);
    },
    { immediate: true },
);

onBeforeUnmount(() => {
    window.removeEventListener("keydown", onKeydown);
});

function onBackdrop(ev: MouseEvent) {
    if (ev.target === ev.currentTarget && props.dismissible) emit("close");
}
</script>

<template>
    <Teleport to="body">
        <Transition enter-active-class="transition-opacity duration-150" enter-from-class="opacity-0"
            enter-to-class="opacity-100" leave-active-class="transition-opacity duration-100"
            leave-from-class="opacity-100" leave-to-class="opacity-0">
            <div v-if="open"
                class="fixed inset-0 z-50 flex items-center justify-center bg-on-background/40 p-4 backdrop-blur-sm"
                role="dialog" aria-modal="true" :data-testid="testid" @click="onBackdrop">
                <slot />
            </div>
        </Transition>
    </Teleport>
</template>
