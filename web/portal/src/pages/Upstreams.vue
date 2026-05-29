<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { UpstreamSummary } from '@gen/limen/portal/v1/portal_pb.js'
import { useUpstreamsStore } from '@/stores/upstreams'
import UpstreamCard from '@/components/UpstreamCard.vue'
import { ApiKeyModal } from '@limen/shared'
import type { CTAKind } from '@limen/shared'

const upstreams = useUpstreamsStore()

const busyRow = ref<string | null>(null)

const modal = ref<{ open: boolean; upstream: UpstreamSummary | null }>({
  open: false,
  upstream: null,
})

onMounted(() => {
  void upstreams.refresh()
})

async function handleAction(up: UpstreamSummary, kind: CTAKind) {
  busyRow.value = up.publicId
  try {
    switch (kind) {
      case 'connect': {
        const url = await upstreams.startConnect(up.identifier, window.location.pathname)
        window.location.assign(url)
        return
      }
      case 'submitKey':
      case 'rotateKey':
        modal.value = { open: true, upstream: up }
        return
      case 'enable':
        await upstreams.setEnabled(up.identifier, true)
        return
      case 'disable':
        await upstreams.setEnabled(up.identifier, false)
        return
      case 'disconnect':
        if (
          !window.confirm(
            `Disconnect ${up.displayName || up.identifier}? This removes stored credentials.`,
          )
        ) {
          return
        }
        await upstreams.disconnect(up.identifier)
        return
    }
  } finally {
    busyRow.value = null
  }
}

async function submitApiKey(apiKey: string) {
  const up = modal.value.upstream
  if (!up) return
  busyRow.value = up.publicId
  try {
    await upstreams.submitApiKey(up.identifier, apiKey)
    modal.value = { open: false, upstream: null }
  } finally {
    busyRow.value = null
  }
}

function cancelModal() {
  modal.value = { open: false, upstream: null }
}
</script>

<template>
  <section>
    <h1 class="text-2xl font-semibold">MCP Servers</h1>
    <p class="mt-1 text-sm text-slate-500">
      Connect your account to each MCP server the tenant exposes. Disabled links keep credentials
      but hide tools; disconnecting drops them. Expand a card to inspect its tool catalog and any
      prefix aliases the gateway discovered.
    </p>

    <p v-if="upstreams.loading" class="mt-4 text-sm text-slate-500" data-state="loading">
      Loading…
    </p>
    <p v-else-if="upstreams.error" class="mt-4 text-sm text-rose-600" data-state="error">
      {{ upstreams.error }}
    </p>
    <p
      v-else-if="upstreams.items.length === 0"
      class="mt-4 text-sm text-slate-500"
      data-state="empty"
    >
      No MCP servers configured for this tenant.
    </p>
    <div v-else class="mt-4 grid gap-3">
      <UpstreamCard
        v-for="up in upstreams.items"
        :key="up.publicId"
        :upstream="up"
        :busy="busyRow === up.publicId"
        @action="(kind) => handleAction(up, kind)"
      />
    </div>

    <ApiKeyModal
      :open="modal.open"
      :upstream-label="modal.upstream?.displayName || modal.upstream?.identifier || ''"
      :busy="busyRow !== null"
      @submit="submitApiKey"
      @cancel="cancelModal"
    />
  </section>
</template>
