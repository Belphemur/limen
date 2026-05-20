<script setup lang="ts">
import { ShieldAlert } from '@lucide/vue'
import { useSessionStore } from '@limen/shared/session'

const session = useSessionStore()
</script>

<template>
  <main class="grid min-h-dvh place-items-center bg-bg p-6 text-text">
    <div class="max-w-md text-center">
      <div
        class="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-danger/10 text-danger"
      >
        <ShieldAlert class="h-6 w-6" :stroke-width="1.5" />
      </div>
      <h1 class="text-display-1 font-display text-text">Access restricted</h1>
      <p class="mt-2 text-body-md text-text-muted">
        The admin console is reserved for owners and admins of this tenant. If you need access, ask
        an existing admin to invite you with the right role.
      </p>
      <button
        type="button"
        class="mt-6 inline-flex h-10 items-center rounded-md bg-surface-2 px-4 text-body-sm font-medium text-text shadow-soft transition hover:bg-surface-3"
        @click="session.logout()"
      >
        Sign out
      </button>
    </div>
  </main>
</template>
