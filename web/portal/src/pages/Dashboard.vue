<script setup lang="ts">
import { computed, ref } from 'vue'
import { Server, Code2, Copy, CheckCircle2 } from '@lucide/vue'
import { tenantPrefix, useSessionStore } from '@limen/shared/session'
import { IDEExamples } from '@limen/shared'

const session = useSessionStore()

const mcpUrl = computed(() => {
  const prefix = tenantPrefix() ?? ''
  return `${window.location.origin}${prefix}/mcp`
})

const copied = ref(false)
async function copyUrl() {
  try {
    await navigator.clipboard.writeText(mcpUrl.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 2000)
  } catch {
    copied.value = false
  }
}
</script>

<template>
  <div class="space-y-stack-lg">
    <header>
      <h1 class="font-display text-3xl font-bold tracking-tight text-on-surface">
        Welcome to Limen
      </h1>
      <p v-if="session.user" class="mt-1 text-sm text-on-surface-variant">
        Signed in as <span class="font-medium text-on-surface">{{ session.user.email }}</span>
        <span v-if="session.role !== 'unspecified'"> · {{ session.role }}</span>
      </p>
      <p class="mt-3 max-w-3xl text-sm text-on-surface-variant">
        Limen is your team's MCP gateway. Connect your personal accounts to each MCP server your
        organization has set up, then plug the gateway URL into your AI IDE — every tool from every
        server becomes available in one place.
      </p>
    </header>

    <!-- Two-step onboarding strip -->
    <section
      aria-label="Onboarding steps"
      class="grid gap-gutter md:grid-cols-2"
      data-testid="portal-onboarding"
    >
      <article
        class="flex flex-col gap-stack-sm rounded-xl border border-outline-variant bg-surface-container-lowest p-5"
        data-testid="step-connect"
      >
        <div class="flex items-center gap-3">
          <span
            class="flex h-9 w-9 items-center justify-center rounded-full bg-primary/10 text-primary"
          >
            <Server :size="18" aria-hidden="true" />
          </span>
          <h2 class="text-base font-semibold text-on-surface">
            1. Connect &amp; verify your MCP servers
          </h2>
        </div>
        <p class="text-sm text-on-surface-variant">
          Sign in to each upstream so the gateway can call its tools on your behalf. Verify every
          server is connected before wiring up your IDE — a missing link means the matching tools
          will be unavailable.
        </p>
        <RouterLink
          to="/mcp-servers"
          class="mt-auto inline-flex w-fit items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow-sm hover:bg-primary-container"
        >
          Open MCP Servers
        </RouterLink>
      </article>

      <article
        class="flex flex-col gap-stack-sm rounded-xl border border-outline-variant bg-surface-container-lowest p-5"
        data-testid="step-ide"
      >
        <div class="flex items-center gap-3">
          <span
            class="flex h-9 w-9 items-center justify-center rounded-full bg-primary/10 text-primary"
          >
            <Code2 :size="18" aria-hidden="true" />
          </span>
          <h2 class="text-base font-semibold text-on-surface">2. Configure your IDE</h2>
        </div>
        <p class="text-sm text-on-surface-variant">
          Point your AI IDE at the gateway URL below. Limen is OAuth 2.0 DCR compliant (RFC 7591),
          so each IDE registers itself on first connect — no client IDs or secrets to copy.
        </p>
        <a
          href="#ide-examples"
          class="mt-auto inline-flex w-fit items-center gap-1.5 rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm font-medium text-on-surface hover:bg-surface-container-low"
        >
          Jump to IDE snippets
        </a>
      </article>
    </section>

    <!-- Gateway URL -->
    <section
      aria-labelledby="portal-gateway-url"
      class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
      data-testid="portal-section-gateway-url"
    >
      <h2 id="portal-gateway-url" class="text-lg font-semibold text-on-surface">
        Your gateway URL
      </h2>
      <p class="text-sm text-on-surface-variant">
        Every IDE for this tenant connects to this single endpoint.
      </p>
      <div class="flex items-center gap-2">
        <code
          class="flex-1 rounded border border-outline-variant bg-surface-variant px-3 py-2 font-mono text-sm text-on-surface break-all"
          data-testid="portal-gateway-url-value"
          >{{ mcpUrl }}</code
        >
        <button
          type="button"
          class="inline-flex items-center gap-1 rounded border border-outline px-3 py-2 text-sm text-on-surface hover:bg-surface-variant"
          data-testid="portal-gateway-url-copy"
          @click="copyUrl"
        >
          <component :is="copied ? CheckCircle2 : Copy" class="h-4 w-4" />
          {{ copied ? 'Copied' : 'Copy' }}
        </button>
      </div>
    </section>

    <!-- IDE Examples -->
    <section
      id="ide-examples"
      aria-labelledby="portal-examples-heading"
      class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
      data-testid="portal-section-examples"
    >
      <h2 id="portal-examples-heading" class="text-lg font-semibold text-on-surface">
        IDE configuration snippets
      </h2>
      <p class="text-sm text-on-surface-variant">
        Drop one of these into the matching IDE's config. Field names track each IDE's current
        documentation — check the vendor docs if a release renames a key.
      </p>
      <IDEExamples :mcp-url="mcpUrl" />
    </section>
  </div>
</template>
