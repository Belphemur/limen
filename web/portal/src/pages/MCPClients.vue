<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { MCPClient } from '@gen/limen/portal/v1/portal_pb.js'
import { portalClient } from '../api/client'

const clients = ref<MCPClient[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const busyId = ref<string | null>(null)

async function refresh(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const resp = await portalClient().listMCPClients({})
    clients.value = resp.clients
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function revoke(c: MCPClient): Promise<void> {
  if (
    !window.confirm(
      `Revoke MCP client "${c.name}"? Existing tokens issued by Zitadel for this client will stop working.`,
    )
  ) {
    return
  }
  busyId.value = c.publicId
  try {
    await portalClient().revokeMCPClient({ publicId: c.publicId })
    await refresh()
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    busyId.value = null
  }
}

onMounted(() => {
  void refresh()
})
</script>

<template>
  <section>
    <h1 class="text-2xl font-semibold">MCP Clients</h1>
    <p class="mt-1 text-sm text-slate-500">
      These applications registered themselves via Dynamic Client Registration through Limen's OAuth
      proxy. Revoking one removes the OIDC app in Zitadel and the local mirror row.
    </p>

    <p v-if="loading" class="mt-4 text-sm text-slate-500" data-state="loading">Loading…</p>
    <p v-else-if="error" class="mt-4 text-sm text-rose-600" data-state="error">{{ error }}</p>
    <p v-else-if="clients.length === 0" class="mt-4 text-sm text-slate-500" data-state="empty">
      No MCP clients have registered yet. They appear automatically after Dynamic Client
      Registration.
    </p>
    <ul v-else class="mt-4 space-y-2">
      <li
        v-for="c in clients"
        :key="c.publicId"
        class="flex items-start justify-between gap-3 rounded-md border border-slate-200 bg-white p-3 dark:border-slate-700 dark:bg-slate-800"
        :data-client-id="c.clientId"
      >
        <div class="min-w-0">
          <p class="truncate font-medium">{{ c.name }}</p>
          <p class="text-xs text-slate-500">
            client_id={{ c.clientId }}<span v-if="c.softwareId"> · {{ c.softwareId }}</span>
            <span v-if="c.softwareVersion"> v{{ c.softwareVersion }}</span>
            · registered {{ c.createdAt }}
          </p>
        </div>
        <button
          type="button"
          class="shrink-0 rounded-md bg-rose-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-rose-500 disabled:opacity-50"
          :disabled="busyId === c.publicId"
          data-cta="revoke"
          @click="revoke(c)"
        >
          Revoke
        </button>
      </li>
    </ul>
  </section>
</template>
