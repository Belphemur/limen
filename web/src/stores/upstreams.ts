import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { UpstreamSummary } from '@gen/limen/portal/v1/portal_pb.js'
import { portalClient } from '../api/client'

// useUpstreamsStore caches the ListUpstreams response and exposes
// mutation helpers that re-fetch on success so the UI never goes
// stale. Slice 5 only models loading state + storage; slice 6 wires
// the page components to it.
export const useUpstreamsStore = defineStore('upstreams', () => {
  const items = ref<UpstreamSummary[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function refresh(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const resp = await portalClient().listUpstreams({})
      items.value = resp.upstreams
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err)
    } finally {
      loading.value = false
    }
  }

  async function startConnect(upstreamName: string, returnTo: string): Promise<string> {
    const resp = await portalClient().startConnect({ upstreamName, returnTo })
    return resp.redirectUrl
  }

  async function submitApiKey(upstreamName: string, apiKey: string): Promise<void> {
    await portalClient().submitUpstreamAPIKey({ upstreamName, apiKey })
    await refresh()
  }

  async function setEnabled(upstreamName: string, enabled: boolean): Promise<void> {
    await portalClient().setUpstreamLinkEnabled({ upstreamName, enabled })
    await refresh()
  }

  async function disconnect(upstreamName: string): Promise<void> {
    await portalClient().disconnect({ upstreamName })
    await refresh()
  }

  return { items, loading, error, refresh, startConnect, submitApiKey, setEnabled, disconnect }
})
