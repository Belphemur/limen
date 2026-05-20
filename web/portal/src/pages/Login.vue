<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useSessionStore } from '../stores/session'

const session = useSessionStore()
const route = useRoute()

// Build the /auth/login URL with tenant + return_to. Phase 4 expects
// both query parameters; the tenant comes from the URL prefix.
const loginHref = computed(() => {
  const match = window.location.pathname.match(/^\/t\/([^/]+)\//)
  const tenant = match ? match[1] : 'dev'
  const returnTo = (route.query.return_to as string | undefined) ?? '/'
  return `/t/${tenant}/auth/login?tenant=${encodeURIComponent(tenant)}&return_to=${encodeURIComponent(returnTo)}`
})
</script>

<template>
  <div class="mx-auto mt-24 max-w-md text-center">
    <h1 class="text-2xl font-semibold">Welcome to Limen</h1>
    <p class="mt-2 text-sm text-slate-500">
      Sign in with your Zitadel account to manage upstreams and MCP clients.
    </p>
    <a
      :href="session.loginUrl || loginHref"
      class="mt-6 inline-block rounded-md bg-indigo-600 px-6 py-2 text-sm font-semibold text-white hover:bg-indigo-500"
    >
      Sign in with Zitadel
    </a>
  </div>
</template>
