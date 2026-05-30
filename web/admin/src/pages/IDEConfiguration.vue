<script setup lang="ts">
// IDE Configuration — phase 9f.
//
// Hosts the IDE allowlist manager plus copy-paste configuration
// snippets so admins can point each common AI IDE at this tenant's
// MCP gateway URL. Limen is OAuth 2.0 Dynamic Client Registration
// compliant (RFC 7591), so most IDEs only need the gateway URL —
// they'll register themselves on first connect.

import { computed, onMounted, ref } from 'vue'
import { Copy } from '@lucide/vue'
import { tenantPrefix } from '@limen/shared/session'
import { IDEExamples, SuccessModal } from '@limen/shared'
import IDEAllowlistManager from '@/components/IDEAllowlistManager.vue'

const mcpUrl = computed(() => {
  const prefix = tenantPrefix() ?? ''
  return `${window.location.origin}${prefix}/mcp`
})

const portalUrl = computed(() => {
  const prefix = tenantPrefix() ?? ''
  return `${window.location.origin}${prefix}/portal/`
})

const copied = ref(false)

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
  } catch {
    copied.value = false
  }
}

onMounted(() => {
  // No data load — IDEAllowlistManager owns its own state.
})
</script>

<template>
  <div class="space-y-stack-lg">
    <header>
      <h1 class="font-display text-3xl font-bold tracking-tight text-on-surface">
        IDE Configuration
      </h1>
      <p class="mt-2 text-sm text-on-surface-variant">
        Pick the AI IDEs your users will connect from, then point them at the tenant's MCP gateway
        URL below. Limen is OAuth 2.0 Dynamic Client Registration compliant (RFC 7591), so each IDE
        registers itself on first connect — no static client IDs or secrets to distribute.
      </p>
    </header>

    <!-- Gateway URL -->
    <section
      aria-labelledby="gateway-url-heading"
      class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
      data-testid="section-gateway-url"
    >
      <h2 id="gateway-url-heading" class="text-lg font-semibold text-on-surface">Gateway URL</h2>
      <p class="text-sm text-on-surface-variant">
        This is the single endpoint every IDE for this tenant connects to.
      </p>
      <div class="flex items-center gap-2">
        <code
          class="flex-1 rounded border border-outline-variant bg-surface-variant px-3 py-2 font-mono text-sm text-on-surface break-all"
          data-testid="gateway-url-value"
          >{{ mcpUrl }}</code
        >
        <button
          type="button"
          class="inline-flex items-center gap-1 rounded border border-outline px-3 py-2 text-sm text-on-surface hover:bg-surface-variant"
          data-testid="gateway-url-copy"
          @click="copy(mcpUrl)"
        >
          <Copy class="h-4 w-4" />
          Copy
        </button>
      </div>
    </section>

    <!-- Portal URL -->
    <section
      aria-labelledby="portal-url-heading"
      class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
      data-testid="section-portal-url"
    >
      <h2 id="portal-url-heading" class="text-lg font-semibold text-on-surface">
        Share with your team
      </h2>
      <p class="text-sm text-on-surface-variant">
        Send this portal URL to your users. They sign in there to link their personal upstream
        accounts (GitHub, Sentry, Atlassian, …) so the gateway can forward tool calls on their
        behalf.
      </p>
      <div class="flex items-center gap-2">
        <a
          :href="portalUrl"
          target="_blank"
          rel="noopener"
          class="flex-1 rounded border border-outline-variant bg-surface-variant px-3 py-2 font-mono text-sm text-primary underline decoration-dotted underline-offset-2 hover:text-primary-container break-all"
          data-testid="portal-url-value"
          >{{ portalUrl }}</a
        >
        <button
          type="button"
          class="inline-flex items-center gap-1 rounded border border-outline px-3 py-2 text-sm text-on-surface hover:bg-surface-variant"
          data-testid="portal-url-copy"
          @click="copy(portalUrl)"
        >
          <Copy class="h-4 w-4" />
          Copy
        </button>
      </div>
    </section>

    <!-- Allowlist -->
    <section
      aria-labelledby="allowlist-heading"
      class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
      data-testid="section-allowlist"
    >
      <h2 id="allowlist-heading" class="text-lg font-semibold text-on-surface">
        Redirect URI allowlist
      </h2>
      <p class="text-sm text-on-surface-variant">
        Each preset adds the official redirect URIs the IDE will register via DCR. The global HTTPS
        / loopback floor always applies, so you only need to enable the IDEs your team actually
        uses.
      </p>
      <IDEAllowlistManager />
    </section>

    <!-- Examples -->
    <section
      aria-labelledby="examples-heading"
      class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
      data-testid="section-examples"
    >
      <h2 id="examples-heading" class="text-lg font-semibold text-on-surface">
        Configuration examples
      </h2>
      <p class="text-sm text-on-surface-variant">
        Drop these snippets into the matching IDE's config. Field names track each IDE's current
        documentation — check the vendor docs if a release renames a key.
      </p>
      <IDEExamples :mcp-url="mcpUrl" />
    </section>

    <SuccessModal
      :open="copied"
      title="Copied"
      message="The configuration was copied to your clipboard."
      @close="copied = false"
    />
  </div>
</template>
