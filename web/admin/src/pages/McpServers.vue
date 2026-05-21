<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ConnectError, Code } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { Plus, RefreshCw, Trash2, ExternalLink } from '@lucide/vue'
import { adminClient, portalClient } from '@/transport/adminClient'
import {
  DeleteUpstreamRequestSchema,
  ReindexUpstreamCatalogRequestSchema,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import { LinkState, type UpstreamSummary } from '@/gen/limen/portal/v1/portal_pb.ts'
import { ROUTES } from '@/router/routes'

const router = useRouter()
const upstreams = ref<UpstreamSummary[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const busy = ref<Record<string, 'reindex' | 'delete' | undefined>>({})

async function refresh() {
  loading.value = true
  error.value = null
  try {
    const resp = await portalClient().listUpstreams({})
    upstreams.value = resp.upstreams
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

onMounted(refresh)

function detailPath(id: string): string {
  return ROUTES.mcpServerDetail.replace(':id', id)
}

function linkLabel(u: UpstreamSummary): string {
  if (!u.requiresLink) return 'Tenant-mode'
  switch (u.linkState) {
    case LinkState.CONNECTED:
      return 'Connected'
    case LinkState.NEEDS_RELINK:
      return 'Needs relink'
    case LinkState.DISABLED:
    case LinkState.AUTO_DISABLED:
      return 'Disabled'
    default:
      return 'Not connected'
  }
}

function linkClass(u: UpstreamSummary): string {
  if (!u.requiresLink) return 'text-on-surface-variant'
  switch (u.linkState) {
    case LinkState.CONNECTED:
      return 'text-success'
    case LinkState.NEEDS_RELINK:
      return 'text-warning'
    case LinkState.DISABLED:
    case LinkState.AUTO_DISABLED:
      return 'text-error'
    default:
      return 'text-on-surface-variant'
  }
}

async function reindex(u: UpstreamSummary) {
  busy.value = { ...busy.value, [u.publicId]: 'reindex' }
  try {
    const resp = await adminClient().reindexUpstreamCatalog(
      create(ReindexUpstreamCatalogRequestSchema, { publicId: u.publicId }),
    )
    if (resp.upstream) {
      upstreams.value = upstreams.value.map((row) =>
        row.publicId === u.publicId ? resp.upstream! : row,
      )
    }
  } catch (err) {
    const code = err instanceof ConnectError ? err.code : null
    if (code === Code.FailedPrecondition) {
      error.value = `Reindex requires a link. Connect "${u.displayName || u.name}" first.`
    } else {
      error.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    busy.value = { ...busy.value, [u.publicId]: undefined }
  }
}

async function remove(u: UpstreamSummary) {
  const label = u.displayName || u.name
  if (!window.confirm(`Delete upstream "${label}"? This cannot be undone.`)) return
  busy.value = { ...busy.value, [u.publicId]: 'delete' }
  try {
    await adminClient().deleteUpstream(
      create(DeleteUpstreamRequestSchema, { publicId: u.publicId }),
    )
    upstreams.value = upstreams.value.filter((row) => row.publicId !== u.publicId)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    busy.value = { ...busy.value, [u.publicId]: undefined }
  }
}

async function connect(u: UpstreamSummary) {
  try {
    const resp = await portalClient().startConnect({
      upstreamName: u.name,
      returnTo: window.location.pathname,
    })
    if (resp.redirectUrl) {
      window.location.href = resp.redirectUrl
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  }
}

const empty = computed(() => !loading.value && upstreams.value.length === 0)
</script>

<template>
  <div class="space-y-stack-lg">
    <header class="flex items-center justify-between">
      <div>
        <h1 class="font-display text-2xl font-bold tracking-tight text-on-surface">
          MCP Servers
        </h1>
        <p class="mt-1 text-sm text-on-surface-variant">
          Connect MCP upstreams to your tenant. Tools become callable as soon as the
          catalog is indexed.
        </p>
      </div>
      <button
        type="button"
        class="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow-sm hover:bg-primary-container"
        data-testid="add-upstream"
        @click="router.push(ROUTES.mcpServerNew)"
      >
        <Plus :size="16" aria-hidden="true" />
        Add server
      </button>
    </header>

    <div
      v-if="error"
      role="alert"
      class="rounded-md border border-error bg-error/10 px-3 py-2 text-sm text-error"
      data-testid="upstreams-error"
    >
      {{ error }}
    </div>

    <section v-if="loading" class="text-sm text-on-surface-variant">Loading…</section>

    <section
      v-else-if="empty"
      class="flex flex-col items-center justify-center rounded-lg border-2 border-dashed border-border-subtle bg-surface p-12 text-center"
      data-testid="upstreams-empty"
    >
      <h2 class="font-display text-xl font-semibold text-on-surface">No MCP servers yet</h2>
      <p class="mt-2 max-w-md text-sm text-on-surface-variant">
        Add your first upstream to start aggregating tools through Limen.
      </p>
      <button
        type="button"
        class="mt-4 inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow-sm hover:bg-primary-container"
        @click="router.push(ROUTES.mcpServerNew)"
      >
        <Plus :size="16" aria-hidden="true" />
        Add first server
      </button>
    </section>

    <section v-else class="overflow-hidden rounded-lg border border-border-subtle">
      <table class="w-full text-sm">
        <thead class="bg-surface-container text-left text-xs uppercase text-on-surface-variant">
          <tr>
            <th class="px-4 py-2 font-medium">Name</th>
            <th class="px-4 py-2 font-medium">Strategy</th>
            <th class="px-4 py-2 font-medium">Status</th>
            <th class="px-4 py-2 font-medium">Tools</th>
            <th class="px-4 py-2"></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border-subtle bg-surface">
          <tr
            v-for="u in upstreams"
            :key="u.publicId"
            :data-testid="`upstream-row-${u.name}`"
            class="hover:bg-surface-container-low"
          >
            <td class="px-4 py-3">
              <RouterLink
                :to="detailPath(u.publicId)"
                class="font-medium text-on-surface hover:text-primary"
              >
                {{ u.displayName || u.name }}
              </RouterLink>
              <div class="text-xs text-on-surface-variant">{{ u.mcpUrl }}</div>
            </td>
            <td class="px-4 py-3 text-on-surface-variant">
              {{ u.strategyType }}<span v-if="u.strategySubMode"> · {{ u.strategySubMode }}</span>
            </td>
            <td class="px-4 py-3" :class="linkClass(u)">{{ linkLabel(u) }}</td>
            <td class="px-4 py-3 text-on-surface-variant">{{ u.tools.length }}</td>
            <td class="px-4 py-3">
              <div class="flex items-center justify-end gap-1">
                <button
                  v-if="u.requiresLink && u.linkState !== LinkState.CONNECTED"
                  type="button"
                  class="inline-flex items-center gap-1 rounded px-2 py-1 text-xs text-primary hover:bg-primary/10"
                  :data-testid="`upstream-connect-${u.name}`"
                  @click="connect(u)"
                >
                  <ExternalLink :size="14" aria-hidden="true" />
                  Connect
                </button>
                <button
                  type="button"
                  class="inline-flex items-center gap-1 rounded px-2 py-1 text-xs text-on-surface-variant hover:bg-surface-container-low hover:text-on-surface disabled:opacity-50"
                  :disabled="busy[u.publicId] === 'reindex'"
                  :data-testid="`upstream-reindex-${u.name}`"
                  @click="reindex(u)"
                >
                  <RefreshCw :size="14" aria-hidden="true" />
                  Reindex
                </button>
                <button
                  type="button"
                  class="inline-flex items-center gap-1 rounded px-2 py-1 text-xs text-error hover:bg-error/10 disabled:opacity-50"
                  :disabled="busy[u.publicId] === 'delete'"
                  :data-testid="`upstream-delete-${u.name}`"
                  @click="remove(u)"
                >
                  <Trash2 :size="14" aria-hidden="true" />
                  Delete
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>
</template>
