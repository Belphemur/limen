<script setup lang="ts">
import { useSessionStore } from '../stores/session'

const session = useSessionStore()
</script>

<template>
  <section>
    <h1 class="text-2xl font-semibold">Dashboard</h1>
    <p v-if="session.user" class="mt-2 text-slate-600">
      Signed in as <span class="font-medium">{{ session.user.email }}</span>
      <span v-if="session.user.roles.length"> · {{ session.user.roles.join(', ') }}</span>
    </p>
    <div class="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <RouterLink
        to="/upstreams"
        class="rounded-lg border border-slate-200 bg-white p-4 hover:border-indigo-400 dark:border-slate-700 dark:bg-slate-800"
      >
        <h2 class="font-medium">Upstreams</h2>
        <p class="mt-1 text-sm text-slate-500">
          Connect, disconnect, and manage your MCP upstreams.
        </p>
      </RouterLink>
      <RouterLink
        to="/mcp-clients"
        class="rounded-lg border border-slate-200 bg-white p-4 hover:border-indigo-400 dark:border-slate-700 dark:bg-slate-800"
      >
        <h2 class="font-medium">MCP Clients</h2>
        <p class="mt-1 text-sm text-slate-500">
          DCR-registered OIDC apps that consume the Limen gateway.
        </p>
      </RouterLink>
      <RouterLink
        to="/settings"
        class="rounded-lg border border-slate-200 bg-white p-4 hover:border-indigo-400 dark:border-slate-700 dark:bg-slate-800"
      >
        <h2 class="font-medium">Settings</h2>
        <p class="mt-1 text-sm text-slate-500">
          Profile, MFA, and tenant deep-links into Zitadel Console.
        </p>
      </RouterLink>
    </div>
  </section>
</template>
