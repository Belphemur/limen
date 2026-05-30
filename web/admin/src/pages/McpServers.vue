<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ConnectError, Code } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import {
  Filter,
  KeyRound,
  Link2Off,
  Pencil,
  Plus,
  RefreshCw,
  Server,
  Settings,
  Trash2,
} from '@lucide/vue'
import {
  ConfirmDeleteModal,
  faviconUrl,
  onFaviconError,
  Tooltip,
  SecretInputModal,
} from '@limen/shared'
import { adminClient, portalClient } from '@/transport/adminClient'
import {
  DeleteUpstreamRequestSchema,
  ReindexUpstreamCatalogRequestSchema,
  UpdateUpstreamRequestSchema,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import { LinkState, type UpstreamSummary } from '@/gen/limen/portal/v1/portal_pb.ts'
import { ROUTES } from '@/router/routes'

const router = useRouter()
const upstreams = ref<UpstreamSummary[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const busy = ref<Record<string, 'reindex' | 'delete' | 'connect-admin' | 'rotate-key' | undefined>>(
  {},
)
const filter = ref('')
const pendingDelete = ref<UpstreamSummary | null>(null)
const rotateTarget = ref<UpstreamSummary | null>(null)
const rotateBusy = ref(false)

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

onMounted(async () => {
  await refresh()

  // Detect OAuth callback from admin upstream connect flow.
  const url = new URL(window.location.href)
  const code = url.searchParams.get('code')
  const state = url.searchParams.get('state')
  if (code && state) {
    const upstreamPublicId = url.searchParams.get('upstream_public_id') || ''
    try {
      const resp = await adminClient().finishAdminCallback({
        upstreamPublicId,
        callbackQuery: url.search.slice(1),
      })
      // Strip query params and redirect to the captured return_to.
      url.search = ''
      window.location.href = resp.returnTo || url.pathname
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err)
    }
  }
})

function detailPath(id: string): string {
  return ROUTES.mcpServerDetail.replace(':id', id)
}

interface StatusPill {
  label: string
  dotClass: string
  pillClass: string
}

function statusPill(u: UpstreamSummary): StatusPill {
  if (!u.requiresLink) {
    return {
      label: 'Tenant-mode',
      dotClass: 'bg-secondary',
      pillClass: 'bg-surface-container-high text-on-surface-variant',
    }
  }
  // mcp_spec upstreams use tenant-level OAuth — show tenant link state.
  if (u.strategyType === 'mcp_spec') {
    switch (u.tenantLinkState) {
      case LinkState.CONNECTED:
        return {
          label: 'Connected',
          dotClass: 'bg-success',
          pillClass: 'bg-success/10 text-success',
        }
      case LinkState.NEEDS_RELINK:
        return {
          label: 'Needs relink',
          dotClass: 'bg-warning',
          pillClass: 'bg-warning/10 text-warning',
        }
      case LinkState.DISABLED:
      case LinkState.AUTO_DISABLED:
        return {
          label: 'Disabled',
          dotClass: 'bg-error',
          pillClass: 'bg-error-container text-error',
        }
      default:
        return {
          label: 'Not configured',
          dotClass: 'bg-secondary',
          pillClass: 'bg-surface-container-high text-on-surface-variant',
        }
    }
  }
  // static_header — admin view checks tenant link state.
  if (u.strategyType === 'static_header') {
    if (u.hasTenantLink) {
      switch (u.tenantLinkState) {
        case LinkState.CONNECTED:
          return {
            label: 'Configured',
            dotClass: 'bg-success',
            pillClass: 'bg-success/10 text-success',
          }
        case LinkState.NEEDS_RELINK:
          return {
            label: 'Needs relink',
            dotClass: 'bg-warning',
            pillClass: 'bg-warning/10 text-warning',
          }
        case LinkState.DISABLED:
        case LinkState.AUTO_DISABLED:
          return {
            label: 'Disabled',
            dotClass: 'bg-error',
            pillClass: 'bg-error-container text-error',
          }
        default:
          break
      }
    }
    // Tenant secret not yet configured.
    return {
      label: 'Not configured',
      dotClass: 'bg-secondary',
      pillClass: 'bg-surface-container-high text-on-surface-variant',
    }
  }
  // Fallback for other user-link strategies.
  switch (u.linkState) {
    case LinkState.CONNECTED:
      return {
        label: 'Connected',
        dotClass: 'bg-success',
        pillClass: 'bg-success/10 text-success',
      }
    case LinkState.NEEDS_RELINK:
      return {
        label: 'Needs relink',
        dotClass: 'bg-warning',
        pillClass: 'bg-warning/10 text-warning',
      }
    case LinkState.DISABLED:
    case LinkState.AUTO_DISABLED:
      return {
        label: 'Disabled',
        dotClass: 'bg-error',
        pillClass: 'bg-error-container text-error',
      }
    default:
      return {
        label: 'Not connected',
        dotClass: 'bg-secondary',
        pillClass: 'bg-surface-container-high text-on-surface-variant',
      }
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
      error.value = `Reindex requires a link. Connect "${u.displayName || u.identifier}" first.`
    } else {
      error.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    busy.value = { ...busy.value, [u.publicId]: undefined }
  }
}

function requestDelete(u: UpstreamSummary) {
  pendingDelete.value = u
}

async function confirmDelete() {
  const u = pendingDelete.value
  if (!u) return
  busy.value = { ...busy.value, [u.publicId]: 'delete' }
  try {
    await adminClient().deleteUpstream(
      create(DeleteUpstreamRequestSchema, { publicId: u.publicId }),
    )
    upstreams.value = upstreams.value.filter((row) => row.publicId !== u.publicId)
    pendingDelete.value = null
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    busy.value = { ...busy.value, [u.publicId]: undefined }
  }
}

async function connectAdmin(u: UpstreamSummary) {
  busy.value = { ...busy.value, [u.publicId]: 'connect-admin' }
  try {
    const resp = await adminClient().startAdminConnect({
      upstreamPublicId: u.publicId,
      returnTo: window.location.pathname,
    })
    if (resp.redirectUrl) {
      window.location.href = resp.redirectUrl
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    busy.value = { ...busy.value, [u.publicId]: undefined }
  }
}

function openRotateModal(u: UpstreamSummary) {
  rotateTarget.value = u
}

async function confirmRotate(secret: string) {
  const u = rotateTarget.value
  if (!u) return
  rotateBusy.value = true
  try {
    await adminClient().updateUpstream(
      create(UpdateUpstreamRequestSchema, {
        publicId: u.publicId,
        strategyConfig: { value: secret },
      }),
    )
    rotateTarget.value = null
    await refresh()
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    rotateBusy.value = false
  }
}

function cancelRotate() {
  rotateTarget.value = null
}

const filtered = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const rows = !q
    ? upstreams.value
    : upstreams.value.filter(
        (u) =>
          u.identifier.toLowerCase().includes(q) ||
          u.displayName.toLowerCase().includes(q) ||
          u.mcpUrl.toLowerCase().includes(q),
      )
  return rows.map((u) => ({
    ...u,
    favicon: faviconUrl(u.mcpUrl),
  }))
})

const empty = computed(() => !loading.value && upstreams.value.length === 0)
const noMatches = computed(
  () => !loading.value && upstreams.value.length > 0 && filtered.value.length === 0,
)
</script>

<template>
  <div class="space-y-stack-lg">
    <header class="flex flex-col gap-stack-md sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h1 class="font-display text-2xl font-bold tracking-tight text-on-surface">
          MCP Upstream Management
        </h1>
        <p class="mt-1 text-sm text-on-surface-variant">
          Manage and monitor connected Model Context Protocol servers.
        </p>
      </div>
      <Tooltip text="Register a new MCP upstream server">
        <button
          type="button"
          class="inline-flex items-center gap-1.5 rounded-lg bg-primary px-4 py-2 text-sm font-medium text-on-primary shadow-sm transition-all hover:bg-primary/90 active:scale-95"
          data-testid="add-upstream"
          @click="router.push(ROUTES.mcpServerNew)"
        >
          <Plus :size="20" aria-hidden="true" />
          Add New Server
        </button>
      </Tooltip>
    </header>

    <div
      v-if="error"
      role="alert"
      class="rounded-md border border-error bg-error-container px-3 py-2 text-sm text-error"
      data-testid="upstreams-error"
    >
      {{ error }}
    </div>

    <section v-if="loading" class="text-sm text-on-surface-variant">Loading…</section>

    <section
      v-else-if="empty"
      class="flex flex-col items-center justify-center rounded-xl border-2 border-dashed border-border-subtle bg-surface p-12 text-center"
      data-testid="upstreams-empty"
    >
      <h2 class="font-display text-xl font-semibold text-on-surface">No MCP servers yet</h2>
      <p class="mt-2 max-w-md text-sm text-on-surface-variant">
        Add your first upstream to start aggregating tools through Limen.
      </p>
      <button
        type="button"
        class="mt-4 inline-flex items-center gap-1.5 rounded-lg bg-primary px-4 py-2 text-sm font-medium text-on-primary shadow-sm transition-all hover:bg-primary/90 active:scale-95"
        @click="router.push(ROUTES.mcpServerNew)"
      >
        <Plus :size="20" aria-hidden="true" />
        Add first server
      </button>
    </section>

    <section
      v-else
      class="overflow-hidden rounded-xl border border-border-subtle bg-surface-container-lowest shadow-sm"
    >
      <!-- Toolbar -->
      <div
        class="flex flex-col gap-stack-md border-b border-border-subtle bg-surface/50 px-6 py-4 sm:flex-row sm:items-center sm:justify-between"
      >
        <div class="relative w-full sm:w-72">
          <Filter
            :size="18"
            aria-hidden="true"
            class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant"
          />
          <input
            v-model="filter"
            type="text"
            placeholder="Filter servers…"
            data-testid="upstreams-filter"
            class="w-full rounded-lg border border-border-subtle bg-surface py-1.5 pl-10 pr-3 text-sm text-on-surface placeholder:text-on-surface-variant focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
          />
        </div>
        <span class="text-xs text-on-surface-variant">
          {{ filtered.length }} of {{ upstreams.length }}
        </span>
      </div>

      <!-- Table -->
      <div class="overflow-x-auto">
        <table class="w-full border-collapse text-left">
          <thead>
            <tr class="border-b border-border-subtle bg-surface-container">
              <th
                class="px-6 py-3 text-xs font-semibold uppercase tracking-wider text-on-surface-variant"
              >
                Server
              </th>
              <th
                class="px-6 py-3 text-xs font-semibold uppercase tracking-wider text-on-surface-variant"
              >
                Endpoint URL
              </th>
              <th
                class="px-6 py-3 text-xs font-semibold uppercase tracking-wider text-on-surface-variant"
              >
                Type
              </th>
              <th
                class="px-6 py-3 text-xs font-semibold uppercase tracking-wider text-on-surface-variant"
              >
                Status
              </th>
              <th
                class="px-6 py-3 text-xs font-semibold uppercase tracking-wider text-on-surface-variant"
              >
                Tools
              </th>
              <th
                class="px-6 py-3 text-right text-xs font-semibold uppercase tracking-wider text-on-surface-variant"
              >
                Actions
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border-subtle text-sm">
            <tr v-if="noMatches">
              <td
                colspan="6"
                class="px-6 py-8 text-center text-sm text-on-surface-variant"
                data-testid="upstreams-no-matches"
              >
                No servers match "{{ filter }}".
              </td>
            </tr>
            <tr
              v-for="u in filtered"
              :key="u.publicId"
              :data-testid="`upstream-row-${u.identifier}`"
              class="group transition-colors hover:bg-surface-container-low"
            >
              <!-- Server -->
              <td class="px-6 py-4">
                <RouterLink :to="detailPath(u.publicId)" class="flex items-center gap-3 group/link">
                  <div
                    class="flex h-8 w-8 shrink-0 items-center justify-center overflow-hidden rounded bg-surface-container-high text-primary"
                  >
                    <img
                      v-if="u.favicon"
                      :src="u.favicon"
                      alt=""
                      class="h-5 w-5 object-contain"
                      loading="lazy"
                      referrerpolicy="no-referrer"
                      @error="onFaviconError"
                    />
                    <Server v-else :size="18" aria-hidden="true" />
                  </div>
                  <div class="min-w-0">
                    <div class="truncate font-medium text-on-surface group-hover/link:text-primary">
                      {{ u.displayName || u.identifier }}
                    </div>
                    <div
                      v-if="u.displayName"
                      class="truncate font-mono text-xs text-on-surface-variant"
                    >
                      {{ u.identifier }}
                    </div>
                  </div>
                </RouterLink>
              </td>
              <!-- URL -->
              <td class="px-6 py-4 font-mono text-xs text-on-surface-variant">
                <span class="block max-w-md truncate">{{ u.mcpUrl }}</span>
              </td>
              <!-- Type -->
              <td class="px-6 py-4">
                <span
                  v-if="u.strategyType === 'mcp_spec'"
                  class="inline-flex items-center rounded-full bg-primary/10 px-2.5 py-0.5 text-xs font-medium text-primary"
                >
                  OAuth2
                </span>
                <span
                  v-else-if="
                    u.strategyType === 'static_header' && u.strategySubMode === 'tenant_owner'
                  "
                  class="inline-flex items-center rounded-full bg-amber-100 px-2.5 py-0.5 text-xs font-medium text-amber-800 dark:bg-amber-900/30 dark:text-amber-400"
                >
                  Static (Shared)
                </span>
                <span
                  v-else-if="u.strategyType === 'static_header' && u.strategySubMode === 'byok'"
                  class="inline-flex items-center rounded-full bg-amber-100 px-2.5 py-0.5 text-xs font-medium text-amber-800 dark:bg-amber-900/30 dark:text-amber-400"
                >
                  Static (BYOK)
                </span>
                <span v-else class="text-xs text-on-surface-variant">{{ u.strategyType }}</span>
              </td>
              <!-- Status -->
              <td class="px-6 py-4">
                <span
                  class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium"
                  :class="statusPill(u).pillClass"
                >
                  <span class="h-1.5 w-1.5 rounded-full" :class="statusPill(u).dotClass" />
                  {{ statusPill(u).label }}
                </span>
              </td>
              <!-- Tools -->
              <td class="px-6 py-4 tabular-nums text-on-surface-variant">
                {{ u.tools.length }}
              </td>
              <!-- Actions -->
              <td class="px-6 py-4">
                <div class="flex items-center justify-end gap-1">
                  <!-- Admin / tenant-level actions -->
                  <Tooltip
                    v-if="
                      u.strategyType === 'mcp_spec' &&
                      (u.tenantLinkState === LinkState.NEEDS_RELINK || !u.hasTenantLink)
                    "
                    :text="!u.hasTenantLink ? 'Configure OAuth' : 'Reconfigure OAuth'"
                  >
                    <button
                      type="button"
                      class="rounded-md p-1.5 text-on-surface-variant transition-colors hover:bg-primary/10 hover:text-primary disabled:opacity-40"
                      :disabled="busy[u.publicId] === 'connect-admin'"
                      :data-testid="`upstream-configure-${u.identifier}`"
                      @click="connectAdmin(u)"
                    >
                      <Settings
                        :size="18"
                        aria-hidden="true"
                        :class="busy[u.publicId] === 'connect-admin' ? 'animate-spin' : ''"
                      />
                    </button>
                  </Tooltip>
                  <Tooltip
                    v-else-if="
                      u.strategyType === 'static_header' && u.strategySubMode === 'tenant_owner'
                    "
                    text="Rotate shared secret"
                  >
                    <button
                      type="button"
                      class="rounded-md p-1.5 text-on-surface-variant transition-colors hover:bg-primary/10 hover:text-primary disabled:opacity-40"
                      :disabled="busy[u.publicId] === 'rotate-key'"
                      :data-testid="`upstream-rotate-key-${u.identifier}`"
                      @click="openRotateModal(u)"
                    >
                      <KeyRound
                        :size="18"
                        aria-hidden="true"
                        :class="busy[u.publicId] === 'rotate-key' ? 'animate-spin' : ''"
                      />
                    </button>
                  </Tooltip>
                  <Tooltip
                    v-else-if="u.strategyType === 'static_header' && u.strategySubMode === 'byok'"
                    text="Rotate setup key"
                  >
                    <button
                      type="button"
                      class="rounded-md p-1.5 text-on-surface-variant transition-colors hover:bg-primary/10 hover:text-primary disabled:opacity-40"
                      :disabled="busy[u.publicId] === 'rotate-key'"
                      :data-testid="`upstream-rotate-setup-key-${u.identifier}`"
                      @click="openRotateModal(u)"
                    >
                      <KeyRound
                        :size="18"
                        aria-hidden="true"
                        :class="busy[u.publicId] === 'rotate-key' ? 'animate-spin' : ''"
                      />
                    </button>
                  </Tooltip>

                  <!-- Per-user link actions -->
                  <Tooltip text="Connected">
                    <button
                      v-if="u.linkState === LinkState.CONNECTED"
                      type="button"
                      disabled
                      class="rounded-md p-1.5 text-on-surface-variant opacity-40"
                    >
                      <Link2Off :size="18" aria-hidden="true" />
                    </button>
                  </Tooltip>
                  <Tooltip text="Edit">
                    <button
                      type="button"
                      class="rounded-md p-1.5 text-on-surface-variant transition-colors hover:bg-primary/10 hover:text-primary"
                      @click="router.push(detailPath(u.publicId))"
                    >
                      <Pencil :size="18" aria-hidden="true" />
                    </button>
                  </Tooltip>
                  <Tooltip text="Reindex catalog">
                    <button
                      type="button"
                      class="rounded-md p-1.5 text-on-surface-variant transition-colors hover:bg-surface-container-high hover:text-on-surface disabled:opacity-40"
                      :disabled="busy[u.publicId] === 'reindex'"
                      :data-testid="`upstream-reindex-${u.identifier}`"
                      @click="reindex(u)"
                    >
                      <RefreshCw
                        :size="18"
                        aria-hidden="true"
                        :class="busy[u.publicId] === 'reindex' ? 'animate-spin' : ''"
                      />
                    </button>
                  </Tooltip>
                  <Tooltip text="Delete">
                    <button
                      type="button"
                      class="rounded-md p-1.5 text-on-surface-variant transition-colors hover:bg-error-container hover:text-error disabled:opacity-40"
                      :disabled="busy[u.publicId] === 'delete'"
                      :data-testid="`upstream-delete-${u.identifier}`"
                      @click="requestDelete(u)"
                    >
                      <Trash2 :size="18" aria-hidden="true" />
                    </button>
                  </Tooltip>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Footer -->
      <div
        class="flex items-center justify-between border-t border-border-subtle bg-surface/50 px-6 py-3 text-xs text-on-surface-variant"
      >
        <span>Showing {{ filtered.length }} of {{ upstreams.length }} entries</span>
      </div>
    </section>

    <ConfirmDeleteModal
      :open="pendingDelete !== null"
      title="Delete upstream"
      :message="`This will permanently remove &quot;${pendingDelete?.displayName || pendingDelete?.identifier}&quot; and revoke its stored credentials. This cannot be undone.`"
      :confirm-token="pendingDelete?.identifier ?? ''"
      confirm-label="Delete upstream"
      :busy="pendingDelete ? busy[pendingDelete.publicId] === 'delete' : false"
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />

    <SecretInputModal
      title="Rotate Shared Secret"
      :description="`Enter a new secret for '${rotateTarget?.displayName || rotateTarget?.identifier || ''}'`"
      label="New secret"
      :open="rotateTarget !== null"
      :busy="rotateBusy"
      @confirm="confirmRotate"
      @cancel="cancelRotate"
    />
  </div>
</template>
