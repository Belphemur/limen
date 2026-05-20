<script setup lang="ts">
import { useSessionStore } from '../stores/session'

const session = useSessionStore()
</script>

<template>
  <section>
    <h1 class="text-2xl font-semibold">Settings</h1>
    <p class="mt-2 text-sm text-slate-500">
      Identity management lives in Zitadel Console. Use the links below to manage your profile, MFA,
      and tenant membership.
    </p>
    <div class="mt-6 grid gap-3 sm:grid-cols-2">
      <a
        href="/auth/logout"
        class="rounded-md border border-slate-200 bg-white p-3 hover:border-rose-400 dark:border-slate-700 dark:bg-slate-800"
      >
        <p class="font-medium">Sign out</p>
        <p class="mt-1 text-xs text-slate-500">
          Ends the portal session and signs you out of Zitadel.
        </p>
      </a>
    </div>
    <p v-if="session.user" class="mt-6 text-xs text-slate-500">
      subject: {{ session.user.subject }}
    </p>
  </section>
</template>
