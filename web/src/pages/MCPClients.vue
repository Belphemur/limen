<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { MCPClient } from '@gen/limen/portal/v1/portal_pb.js'
import { portalClient } from '../api/client'

const clients = ref<MCPClient[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

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

onMounted(() => {
  void refresh()
})
</script>

<template>
  <section>
    <h1 class="text-2xl font-semibold">MCP Clients</h1>
    <p v-if="loading" class="mt-4 text-sm text-slate-500">Loading…</p>
    <p v-else-if="error" class="mt-4 text-sm text-rose-600">{{ error }}</p>
    <p v-else-if="clients.length === 0" class="mt-4 text-sm text-slate-500">
      No MCP clients have registered yet. They appear automatically after Dynamic Client
      Registration.
    </p>
    <ul v-else class="mt-4 space-y-2">
      <li
        v-for="c in clients"
        :key="c.publicId"
        class="rounded-md border border-slate-200 bg-white p-3 dark:border-slate-700 dark:bg-slate-800"
      >
        <p class="font-medium">{{ c.name }}</p>
        <p class="text-xs text-slate-500">
          client_id={{ c.clientId }} · registered {{ c.createdAt }}
        </p>
      </li>
    </ul>
  </section>
</template>
