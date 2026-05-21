<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { CheckCircle2, AlertCircle } from '@lucide/vue'
import { postOAuthPopupResultAndClose, type OAuthPopupResult } from '@limen/shared'

const result = ref<OAuthPopupResult | null>(null)

onMounted(() => {
  result.value = postOAuthPopupResultAndClose()
})
</script>

<template>
  <div class="flex min-h-screen items-center justify-center bg-bg-main p-8">
    <div
      class="flex w-full max-w-md flex-col items-center rounded-xl border border-border-light bg-surface p-8 text-center shadow-sm"
    >
      <template v-if="result?.ok">
        <div class="mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-success/20">
          <CheckCircle2 :size="36" class="text-success" aria-hidden="true" />
        </div>
        <h1 class="mb-2 font-headline-lg text-headline-lg text-on-surface">Authorized</h1>
        <p class="font-body-md text-body-md text-on-surface-variant">
          You can close this window — we'll finish setting things up for you.
        </p>
      </template>
      <template v-else-if="result">
        <div
          class="mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-error-container"
        >
          <AlertCircle :size="36" class="text-error" aria-hidden="true" />
        </div>
        <h1 class="mb-2 font-headline-lg text-headline-lg text-on-surface">Authorization failed</h1>
        <p class="font-body-md text-body-md text-on-surface-variant">
          {{ result.errorDescription || result.error || 'The OAuth flow did not complete.' }}
        </p>
        <p class="mt-2 text-xs text-on-surface-variant">You can close this window.</p>
      </template>
    </div>
  </div>
</template>
