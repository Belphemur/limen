<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'

// The SPA can be served at /t/<slug>/ (dev / bare prod) or
// /t/<slug>/portal/ (prod with Caddy file_server prefix). Either way,
// /t/<slug>/auth/login is the entry point the Phase-4 OIDC handler
// is mounted on, and return_to is whatever the SPA wants the browser
// to land on after the callback.
//
// Source of truth for the post-login destination is the `return_to`
// query the navigation guard set when it bounced the user here — the
// browser's current pathname is the login page itself, so reading it
// would loop the user back to /login after the callback.
//
// When the user is *not* under a /t/<slug>/ prefix (root shell or the
// /signed-out page), we fall back to the tenant-agnostic /auth/login
// endpoint. The callback resolves the tenant from the token's Zitadel
// home-org claim and redirects into /t/<resolved>/.
const route = useRoute()

function tenantPrefixFromPath(path: string): string | null {
  const m = path.match(/^(\/t\/[^/]+)(?:\/.*)?$/)
  return m ? m[1] : null
}

const loginHref = computed(() => {
  // Prefer the guard-provided return_to; fall back to current pathname
  // (covers the "user clicked the bookmarked /t/<slug>/login URL while
  // already signed out" case).
  const raw = route.query.return_to
  const returnToParam = typeof raw === 'string' && raw ? raw : null

  // The router base strips the /t/<slug>/ prefix from `route.query`, so
  // pull the tenant from the live pathname; the return_to itself is a
  // SPA-relative path like "/mcp-servers".
  const tenantPrefix = tenantPrefixFromPath(window.location.pathname)
  if (!tenantPrefix) {
    return '/auth/login'
  }

  const rest = returnToParam ?? '/'
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
