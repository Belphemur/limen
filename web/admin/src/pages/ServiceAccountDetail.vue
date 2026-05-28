<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { create } from '@bufbuild/protobuf'
import { ArrowLeft, RefreshCw, Save, Trash2, Copy, X, Loader2, Filter, Server } from '@lucide/vue'
import { ConfirmDeleteModal } from '@limen/shared'
import { ConfirmActionModal } from '@limen/shared'
import { adminClient, portalClient } from '@/transport/adminClient'
import {
  GetServiceAccountRequestSchema,
  UpdateServiceAccountRequestSchema,
  ListServiceAccountUpstreamLinksRequestSchema,
  SetServiceAccountLinkEnabledRequestSchema,
  DeleteServiceAccountRequestSchema,
  RegenerateServiceAccountTokenRequestSchema,
  type ServiceAccount,
  type ServiceAccountUpstreamLink,
} from '@/gen/limen/admin/v1/admin_pb'
import { type UpstreamSummary } from '@/gen/limen/portal/v1/portal_pb.ts'
import { ROUTES } from '@/router/routes'
import { formatDate, roleLabel, roleClass } from '@/lib/sa'

const route = useRoute()
const router = useRouter()
const publicId = computed(() => String(route.params.id))

const sa = ref<ServiceAccount | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)

const editName = ref('')
const editDescription = ref('')
const saving = ref(false)

const links = ref<ServiceAccountUpstreamLink[]>([])
const linksLoading = ref(false)
const linksError = ref<string | null>(null)

const allUpstreams = ref<UpstreamSummary[]>([])
const upstreamsLoading = ref(false)
const upstreamsError = ref<string | null>(null)
const filter = ref('')

const showRegenModal = ref(false)
const showRegenerateModal = ref(false)
const showDeleteModal = ref(false)
const regenTarget = ref<{ publicId: string; name: string } | null>(null)
const regenBusy = ref(false)
const newToken = ref('')
const deleteBusy = ref(false)

const copied = ref(false)
let copyTimeout: ReturnType<typeof setTimeout> | null = null

// Map from upstream publicId to link for quick lookup
const linkMap = computed<Map<string, ServiceAccountUpstreamLink>>(() => {
  const m = new Map<string, ServiceAccountUpstreamLink>()
  for (const link of links.value) {
    m.set(link.upstreamPublicId, link)
  }
  return m
})

// Filtered and enriched upstream list
const portalUpstreams = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return allUpstreams.value
    .filter((u) => {
      if (!q) return true
      return (
        (u.displayName || u.identifier).toLowerCase().includes(q) ||
        u.mcpUrl.toLowerCase().includes(q)
      )
    })
    .map((u) => ({
      upstream: u,
      link: linkMap.value.get(u.publicId) ?? null,
    }))
})

async function refresh() {
  loading.value = true
  error.value = null
  try {
    const resp = await adminClient().getServiceAccount(
      create(GetServiceAccountRequestSchema, { publicId: publicId.value }),
    )
    sa.value = resp.serviceAccount ?? null
    if (sa.value) {
      editName.value = sa.value.name
      editDescription.value = sa.value.description
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function loadUpstreams() {
  upstreamsLoading.value = true
  upstreamsError.value = null
  try {
    const resp = await portalClient().listUpstreams({})
    allUpstreams.value = resp.upstreams
  } catch (err) {
    upstreamsError.value = err instanceof Error ? err.message : String(err)
  } finally {
    upstreamsLoading.value = false
  }
}

onMounted(async () => {
  await refresh()
  linksLoading.value = true
  linksError.value = null
  try {
    const resp = await adminClient().listServiceAccountUpstreamLinks(
      create(ListServiceAccountUpstreamLinksRequestSchema, {
        serviceAccountPublicId: publicId.value,
      }),
    )
    links.value = resp.links
  } catch (err) {
    linksError.value = err instanceof Error ? err.message : String(err)
  } finally {
    linksLoading.value = false
  }
  await loadUpstreams()
})

const canSave = computed(
  () =>
    !saving.value &&
    sa.value !== null &&
    (editName.value !== sa.value.name || editDescription.value !== sa.value.description),
)

async function saveDetails() {
  if (!canSave.value || !sa.value) return
  saving.value = true
  error.value = null
  try {
    const resp = await adminClient().updateServiceAccount(
      create(UpdateServiceAccountRequestSchema, {
        publicId: publicId.value,
        name: editName.value !== sa.value.name ? editName.value : undefined,
        description:
          editDescription.value !== sa.value.description ? editDescription.value : undefined,
      }),
    )
    sa.value = resp.serviceAccount ?? sa.value
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    saving.value = false
  }
}

function regenerateToken() {
  if (!sa.value) return
  regenTarget.value = { publicId: sa.value.publicId, name: sa.value.name }
  showRegenModal.value = true
}

async function confirmRegenerate() {
  const target = regenTarget.value
  if (!target || !sa.value) return
  regenBusy.value = true
  error.value = null
  try {
    const resp = await adminClient().regenerateServiceAccountToken(
      create(RegenerateServiceAccountTokenRequestSchema, {
        publicId: target.publicId,
        expiryDays: 365,
      }),
    )
    newToken.value = resp.token
    showRegenModal.value = false
    showRegenerateModal.value = true
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    regenBusy.value = false
  }
}

function cancelRegenerate() {
  showRegenModal.value = false
  regenTarget.value = null
}

async function deleteSA() {
  if (!sa.value) return
  deleteBusy.value = true
  error.value = null
  try {
    await adminClient().deleteServiceAccount(
      create(DeleteServiceAccountRequestSchema, { publicId: publicId.value }),
    )
    await router.push(ROUTES.serviceAccounts)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    deleteBusy.value = false
  }
}

function closeRegenerateModal() {
  showRegenerateModal.value = false
  newToken.value = ''
}

async function copyToken() {
  if (!newToken.value) return
  try {
    await navigator.clipboard.writeText(newToken.value)
    copied.value = true
    if (copyTimeout) clearTimeout(copyTimeout)
    copyTimeout = setTimeout(() => (copied.value = false), 2000)
  } catch {
    copied.value = false
  }
}

async function toggleLink(link: ServiceAccountUpstreamLink) {
  try {
    await adminClient().setServiceAccountLinkEnabled(
      create(SetServiceAccountLinkEnabledRequestSchema, {
        serviceAccountPublicId: publicId.value,
        upstreamPublicId: link.upstreamPublicId,
        enabled: !link.enabled,
      }),
    )
    // Refresh links to reflect the toggled state
    linksLoading.value = true
    linksError.value = null
    const resp = await adminClient().listServiceAccountUpstreamLinks(
      create(ListServiceAccountUpstreamLinksRequestSchema, {
        serviceAccountPublicId: publicId.value,
      }),
    )
    links.value = resp.links
  } catch (err) {
    linksError.value = err instanceof Error ? err.message : String(err)
  } finally {
    linksLoading.value = false
  }
}

// Favicon helper (same as McpServers.vue)
function faviconUrl(mcpUrl: string): string | null {
  try {
    const host = new URL(mcpUrl).hostname
    if (!host) return null
    const parts = host.split('.').filter(Boolean)
    const root = parts.length >= 2 ? parts.slice(-2).join('.') : host
    return `https://www.google.com/s2/favicons?domain=${encodeURIComponent(root)}&sz=64`
  } catch {
    return null
  }
}

function onFaviconError(ev: Event) {
  ;(ev.target as HTMLImageElement).style.display = 'none'
}

interface StatusPill {
  label: string
  dotClass: string
  pillClass: string
}

function linkStatusPill(link: ServiceAccountUpstreamLink | null): StatusPill {
  if (!link) {
    return {
      label: 'Not linked',
      dotClass: 'bg-on-surface-variant',
      pillClass: 'bg-surface-container-high text-on-surface-variant',
    }
  }
  if (link.enabled) {
    return {
      label: 'Enabled',
      dotClass: 'bg-success',
      pillClass: 'bg-success/10 text-success',
    }
  }
  return {
    label: 'Disabled',
    dotClass: 'bg-error',
    pillClass: 'bg-error-container text-error',
  }
}
</script>

<template>
  <div class="space-y-stack-lg">
    <header class="flex items-center gap-3">
      <button
        type="button"
        class="inline-flex items-center gap-1 rounded px-2 py-1 text-sm text-on-surface-variant hover:bg-surface-container-low hover:text-on-surface"
        @click="router.push(ROUTES.serviceAccounts)"
      >
        <ArrowLeft :size="16" aria-hidden="true" />
        Back
      </button>
      <h1 class="font-display text-2xl font-bold tracking-tight text-on-surface">
        {{ sa?.name || 'Service Account' }}
      </h1>
    </header>

    <div
      v-if="error"
      role="alert"
      class="rounded-md border border-error bg-error/10 px-3 py-2 text-sm text-error"
      data-testid="sa-detail-error"
    >
      {{ error }}
    </div>

    <section v-if="loading" class="text-sm text-on-surface-variant">Loading…</section>

    <template v-else-if="sa">
      <!-- Detail header card -->
      <section class="rounded-lg border border-border-subtle bg-surface p-4">
        <dl class="grid gap-stack-sm text-sm md:grid-cols-2">
          <div>
            <dt class="text-on-surface-variant">Public ID</dt>
            <dd class="font-mono text-on-surface">{{ sa.publicId }}</dd>
          </div>
          <div>
            <dt class="text-on-surface-variant">Role</dt>
            <dd>
              <span
                class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium"
                :class="roleClass(sa.role)"
              >
                {{ roleLabel(sa.role) }}
              </span>
            </dd>
          </div>
          <div>
            <dt class="text-on-surface-variant">Created</dt>
            <dd class="text-on-surface">{{ formatDate(sa.createdAt) }}</dd>
          </div>
          <div>
            <dt class="text-on-surface-variant">Created By</dt>
            <dd class="font-mono text-on-surface">{{ sa.createdById || '—' }}</dd>
          </div>
          <div>
            <dt class="text-on-surface-variant">Token Generated</dt>
            <dd class="text-on-surface">
              {{ sa.tokenGeneratedAt ? formatDate(sa.tokenGeneratedAt) : 'Not yet generated' }}
            </dd>
          </div>
          <div>
            <dt class="text-on-surface-variant">Last Used</dt>
            <dd class="text-on-surface">
              {{ sa.lastUsedAt ? formatDate(sa.lastUsedAt) : 'Never used' }}
            </dd>
          </div>
          <div class="md:col-span-2">
            <dt class="text-on-surface-variant">Description</dt>
            <dd class="text-on-surface">{{ sa.description || '—' }}</dd>
          </div>
        </dl>
      </section>

      <!-- Edit section -->
      <section class="space-y-stack-md rounded-lg border border-border-subtle bg-surface p-4">
        <h2 class="font-display text-lg font-semibold text-on-surface">Edit</h2>
        <label class="block">
          <span class="text-sm font-medium text-on-surface">Name</span>
          <input
            v-model="editName"
            type="text"
            class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            data-testid="field-sa-name"
          />
        </label>
        <label class="block">
          <span class="text-sm font-medium text-on-surface">Description</span>
          <textarea
            v-model="editDescription"
            rows="3"
            class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            data-testid="field-sa-description"
          />
        </label>
        <button
          type="button"
          :disabled="!canSave"
          class="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow-sm hover:bg-primary/90 disabled:opacity-50"
          data-testid="save-sa"
          @click="saveDetails"
        >
          <Save :size="16" aria-hidden="true" />
          {{ saving ? 'Saving…' : 'Save changes' }}
        </button>
      </section>

      <!-- MCP Portal section -->
      <section class="space-y-stack-md rounded-lg border border-border-subtle bg-surface p-4">
        <h2 class="font-display text-lg font-semibold text-on-surface">MCP Portal</h2>
        <p class="text-sm text-on-surface-variant">
          Manage which MCP upstreams this service account can access. Linked upstreams can be
          individually enabled or disabled.
        </p>

        <!-- Loading states -->
        <div
          v-if="linksLoading || upstreamsLoading"
          class="flex items-center gap-2 text-sm text-on-surface-variant"
        >
          <Loader2 class="size-4 animate-spin" />
          Loading upstreams…
        </div>
        <div v-else-if="linksError || upstreamsError" class="text-sm text-error">
          {{ linksError || upstreamsError }}
        </div>

        <template v-else>
          <!-- Empty state -->
          <div
            v-if="allUpstreams.length === 0"
            class="py-8 text-center text-sm text-on-surface-variant"
          >
            <div
              class="mx-auto flex size-12 items-center justify-center rounded-full bg-primary/15"
            >
              <Server class="size-6 text-primary" />
            </div>
            <p class="mt-3 font-medium text-on-surface">No MCP upstreams configured</p>
            <p class="mt-1">
              Add upstreams from the
              <router-link :to="ROUTES.mcpServers" class="text-primary hover:underline">
                MCP Servers
              </router-link>
              page first.
            </p>
          </div>

          <!-- Upstream table -->
          <div v-else class="overflow-hidden rounded-lg border border-border-subtle">
            <!-- Filter toolbar -->
            <div
              class="flex items-center gap-3 border-b border-border-subtle bg-surface/50 px-4 py-3"
            >
              <div class="relative w-full sm:w-64">
                <Filter
                  :size="16"
                  aria-hidden="true"
                  class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant"
                />
                <input
                  v-model="filter"
                  type="text"
                  placeholder="Filter upstreams…"
                  class="w-full rounded-md border border-outline-variant bg-surface py-1.5 pl-9 pr-3 text-sm text-on-surface placeholder:text-on-surface-variant focus:border-primary focus:outline-none"
                />
              </div>
              <span class="text-xs text-on-surface-variant">
                {{ portalUpstreams.length }} of {{ allUpstreams.length }}
              </span>
            </div>

            <!-- Table -->
            <div class="overflow-x-auto">
              <table class="w-full text-left text-sm">
                <thead class="bg-surface-container text-xs uppercase text-on-surface-variant">
                  <tr>
                    <th class="px-4 py-3">Upstream</th>
                    <th class="px-4 py-3">Strategy</th>
                    <th class="px-4 py-3">Link Status</th>
                    <th class="px-4 py-3 text-right">Action</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-border-subtle">
                  <tr
                    v-for="item in portalUpstreams"
                    :key="item.upstream.publicId"
                    class="transition-colors hover:bg-surface-container-low"
                  >
                    <!-- Upstream -->
                    <td class="px-4 py-3">
                      <div class="flex items-center gap-3">
                        <div
                          class="flex h-8 w-8 shrink-0 items-center justify-center overflow-hidden rounded bg-surface-container-high text-primary"
                        >
                          <img
                            v-if="faviconUrl(item.upstream.mcpUrl)"
                            :src="faviconUrl(item.upstream.mcpUrl)!"
                            alt=""
                            class="h-5 w-5 object-contain"
                            loading="lazy"
                            referrerpolicy="no-referrer"
                            @error="onFaviconError"
                          />
                          <Server v-else :size="16" aria-hidden="true" />
                        </div>
                        <div class="min-w-0">
                          <div class="truncate font-medium text-on-surface">
                            {{ item.upstream.displayName || item.upstream.identifier }}
                          </div>
                          <div class="truncate font-mono text-xs text-on-surface-variant">
                            {{ item.upstream.identifier }}
                          </div>
                        </div>
                      </div>
                    </td>
                    <!-- Strategy -->
                    <td class="px-4 py-3">
                      <span class="text-on-surface-variant">{{ item.upstream.strategyType }}</span>
                      <span v-if="item.upstream.strategySubMode" class="text-on-surface-variant">
                        · {{ item.upstream.strategySubMode }}
                      </span>
                    </td>
                    <!-- Link Status -->
                    <td class="px-4 py-3">
                      <span
                        class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium"
                        :class="linkStatusPill(item.link).pillClass"
                      >
                        <span
                          class="h-1.5 w-1.5 rounded-full"
                          :class="linkStatusPill(item.link).dotClass"
                        />
                        {{ linkStatusPill(item.link).label }}
                      </span>
                    </td>
                    <!-- Action -->
                    <td class="px-4 py-3 text-right">
                      <button
                        v-if="item.link"
                        type="button"
                        class="inline-flex items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 py-1.5 text-sm font-medium text-on-surface hover:bg-surface-container-low"
                        @click="toggleLink(item.link)"
                      >
                        {{ item.link.enabled ? 'Disable' : 'Enable' }}
                      </button>
                      <span v-else class="text-xs text-on-surface-variant">—</span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </template>
      </section>

      <!-- Danger zone -->
      <section class="space-y-stack-md rounded-lg border border-error/40 bg-error/5 p-4">
        <h2 class="font-display text-lg font-semibold text-error">Danger zone</h2>
        <div class="flex flex-wrap gap-3">
          <button
            type="button"
            class="inline-flex items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 py-2 text-sm font-medium text-on-surface hover:bg-surface-container-low"
            data-testid="regenerate-sa-token"
            @click="regenerateToken"
          >
            <RefreshCw :size="16" aria-hidden="true" />
            Regenerate token
          </button>
          <button
            type="button"
            class="inline-flex items-center gap-1.5 rounded-md bg-error px-3 py-2 text-sm font-medium text-on-error shadow-sm hover:bg-error/90"
            data-testid="delete-sa"
            @click="showDeleteModal = true"
          >
            <Trash2 :size="16" aria-hidden="true" />
            Delete service account
          </button>
        </div>
      </section>
    </template>

    <section v-else class="text-sm text-on-surface-variant">Service account not found.</section>

    <!-- Regenerate token modal -->
    <Teleport to="body">
      <div
        v-if="showRegenerateModal"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
        @click.self="closeRegenerateModal"
      >
        <div
          class="w-full max-w-lg rounded-lg bg-surface p-5 shadow-xl"
          role="dialog"
          aria-modal="true"
        >
          <div class="mb-4 flex items-center justify-between">
            <h2 class="font-display text-lg font-semibold text-on-surface">Token Regenerated</h2>
            <button
              type="button"
              class="rounded p-1 text-on-surface-variant hover:bg-surface-variant"
              @click="closeRegenerateModal"
            >
              <X class="size-4" />
            </button>
          </div>
          <div class="space-y-4">
            <div
              class="rounded border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-amber-700 dark:text-amber-300"
            >
              Copy this token now. It won't be shown again. The old token has been revoked.
            </div>
            <div class="space-y-2 text-sm text-on-surface-variant">
              <p>
                <strong class="text-on-surface">Next:</strong> Use the token with your AI agent or
                CLI tool:
              </p>
              <div class="rounded border border-outline-variant bg-surface-variant p-2">
                <code class="block break-all font-mono text-xs text-on-surface">
                  curl -H "Authorization: Bearer YOUR_TOKEN" https://your-gateway/t/{tenant}/mcp/sse
                </code>
              </div>
            </div>
            <div class="rounded border border-outline-variant bg-surface-variant p-3">
              <code class="block break-all font-mono text-sm text-on-surface">{{ newToken }}</code>
            </div>
            <div class="flex items-center gap-2">
              <button
                type="button"
                class="inline-flex items-center gap-2 rounded-md border border-outline px-3 py-2 text-sm text-on-surface hover:bg-surface-variant"
                data-testid="sa-token-copy"
                @click="copyToken"
              >
                <Copy class="size-4" />
                {{ copied ? 'Copied!' : 'Copy' }}
              </button>
              <button
                type="button"
                class="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow hover:bg-primary/90"
                data-testid="sa-token-done"
                @click="closeRegenerateModal"
              >
                Done
              </button>
            </div>
          </div>
        </div>
      </div>
    </Teleport>

    <ConfirmActionModal
      :open="showRegenModal"
      title="Regenerate Token"
      :message="`This will revoke the existing token for ${regenTarget?.name ?? 'this service account'}. ${regenTarget?.name ?? 'It'} will stop working immediately.`"
      primary-label="Regenerate"
      :busy="regenBusy"
      @confirm="confirmRegenerate"
      @cancel="cancelRegenerate"
    />

    <!-- Delete confirmation -->
    <ConfirmDeleteModal
      :open="showDeleteModal"
      title="Delete Service Account"
      message="This action cannot be undone. Any active token for this service account will stop working immediately."
      confirm-token="delete"
      confirm-label="Delete"
      :busy="deleteBusy"
      @confirm="deleteSA"
      @cancel="showDeleteModal = false"
    />
  </div>
</template>
