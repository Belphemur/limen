<script setup lang="ts">
import { computed, onMounted } from 'vue'

// The SPA can be served at /t/<slug>/ (dev / bare prod) or
// /t/<slug>/portal/ (prod with Caddy file_server prefix). Either way,
// /t/<slug>/auth/login is the entry point the Phase-4 OIDC handler
// is mounted on, and return_to is whatever the SPA wants the browser
// to land on after the callback. Pass the current path so the SPA
// resumes wherever the user clicked Sign in.
//
// When the user is *not* under a /t/<slug>/ prefix (root shell or the
// /signed-out page), we fall back to the tenant-agnostic /auth/login
// endpoint. The callback resolves the tenant from the token's Zitadel
// home-org claim and redirects into /t/<resolved>/.
const loginHref = computed(() => {
  const m = window.location.pathname.match(/^(\/t\/[^/]+)(\/.*)?$/)
  if (!m) {
    return '/auth/login'
  }
  const tenantPrefix = m[1]
  const rest = m[2] || '/'
  return `${tenantPrefix}/auth/login?return_to=${encodeURIComponent(rest)}`
})

const hasTenantInUrl = computed(() => /^\/t\/[^/]+/.test(window.location.pathname))

// Without a tenant in the URL we know exactly where the browser needs
// to go: straight to Zitadel via /auth/login. Skip the click.
onMounted(() => {
  if (!hasTenantInUrl.value) {
    window.location.href = loginHref.value
  }
})
</script>

<template>
  <div class="mx-auto mt-24 max-w-md text-center">
    <h1 class="text-2xl font-semibold">Welcome to Limen</h1>
    <p class="mt-2 text-sm text-slate-500">
      Sign in with your Zitadel account to manage upstreams and MCP clients.
    </p>
    <a :href="loginHref"
      class="mt-6 inline-block rounded-md bg-indigo-600 px-6 py-2 text-sm font-semibold text-white hover:bg-indigo-500">
      Sign in with Zitadel
    </a>
  </div>
</template>
