<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { create } from '@bufbuild/protobuf'
import { ArrowLeft, RefreshCw, Save, Trash2, Copy, X } from '@lucide/vue'
import { adminClient } from '@/transport/adminClient'
import {
  GetServiceAccountRequestSchema,
  UpdateServiceAccountRequestSchema,
  ListServiceAccountUpstreamLinksRequestSchema,
  SetServiceAccountLinkEnabledRequestSchema,
  DeleteServiceAccountRequestSchema,
  RegenerateServiceAccountTokenRequestSchema,
  ServiceAccountRole,
  type ServiceAccount,
  type ServiceAccountUpstreamLink,
} from '@/gen/limen/admin/v1/admin_pb'
import { ConfirmDeleteModal } from '@limen/shared'
import { ROUTES } from '@/router/routes'

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

const showRegenerateModal = ref(false)
const showDeleteModal = ref(false)
const newToken = ref('')
const deleteBusy = ref(false)

const copied = ref(false)
let copyTimeout: ReturnType<typeof setTimeout> | null = null

function roleLabel(role: ServiceAccountRole): string {
  switch (role) {
    case ServiceAccountRole.ADMIN:
      return 'Admin'
    case ServiceAccountRole.MEMBER:
      return 'Member'
    default:
      return 'Unknown'
  }
}

function roleClass(role: ServiceAccountRole): string {
  switch (role) {
    case ServiceAccountRole.ADMIN:
      return 'bg-primary/15 text-primary'
    case ServiceAccountRole.MEMBER:
      return 'bg-surface-variant text-on-surface-variant'
    default:
      return 'bg-surface-variant text-on-surface-variant'
  }
}

function formatDate(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' })
}

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
        description: editDescription.value !== sa.value.description ? editDescription.value : undefined,
      }),
    )
    sa.value = resp.serviceAccount ?? sa.value
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    saving.value = false
  }
}

async function regenerateToken() {
  if (!sa.value) return
  error.value = null
  try {
    const resp = await adminClient().regenerateServiceAccountToken(
      create(RegenerateServiceAccountTokenRequestSchema, {
        publicId: publicId.value,
        expiryDays: 365,
      }),
    )
    newToken.value = resp.token
    showRegenerateModal.value = true
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  }
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

      <!-- Upstream Links section -->
      <section class="space-y-stack-md rounded-lg border border-border-subtle bg-surface p-4">
        <h2 class="font-display text-lg font-semibold text-on-surface">Upstream Links</h2>
        <div v-if="linksLoading" class="text-sm text-on-surface-variant">Loading links…</div>
        <div v-else-if="linksError" class="text-sm text-error">{{ linksError }}</div>
        <template v-else>
          <div v-if="links.length === 0" class="text-sm text-on-surface-variant">
            No upstream links configured for this service account.
          </div>
          <ul v-else class="divide-y divide-border-subtle">
            <li
              v-for="link in links"
              :key="String(link.upstreamPublicId)"
              class="flex flex-col gap-2 py-3 sm:flex-row sm:items-center sm:justify-between"
            >
              <div class="space-y-1">
                <div class="font-medium text-on-surface">{{ link.upstreamName }}</div>
                <div class="font-mono text-xs text-on-surface-variant">{{ link.upstreamUrl }}</div>
                <div class="flex items-center gap-2 text-xs">
                  <span
                    class="inline-flex rounded-full px-2 py-0.5 font-medium"
                    :class="
                      link.enabled
                        ? 'bg-success/15 text-success'
                        : 'bg-surface-variant text-on-surface-variant'
                    "
                  >
                    {{ link.enabled ? 'Enabled' : 'Disabled' }}
                  </span>
                  <span class="text-on-surface-variant">Connected {{ link.connectedAt }}</span>
                </div>
              </div>
              <button
                type="button"
                class="inline-flex items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 py-2 text-sm font-medium text-on-surface hover:bg-surface-container-low"
                @click="toggleLink(link)"
              >
                {{ link.enabled ? 'Disable' : 'Enable' }}
              </button>
            </li>
          </ul>
        </template>
      </section>

      <!-- DCR Clients section (placeholder) -->
      <section class="space-y-stack-md rounded-lg border border-border-subtle bg-surface p-4">
        <h2 class="font-display text-lg font-semibold text-on-surface">DCR Clients</h2>
        <p class="text-sm text-on-surface-variant">
          No DCR clients registered. DCR client management will be available in a future update.
        </p>
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
        <div class="w-full max-w-lg rounded-lg bg-surface p-5 shadow-xl" role="dialog" aria-modal="true">
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
