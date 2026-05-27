<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { create } from '@bufbuild/protobuf'
import { ConnectError } from '@connectrpc/connect'
import { adminClient } from '@/transport/adminClient'
import {
  CreateServiceAccountRequestSchema,
  ListServiceAccountsRequestSchema,
  ServiceAccountRole,
  type ServiceAccount,
} from '@/gen/limen/admin/v1/admin_pb'
import { ROUTES } from '@/router/routes'
import { KeyRound, Plus, Copy, X, Loader2 } from '@lucide/vue'

const serviceAccounts = ref<ServiceAccount[]>([])
const loading = ref(true)
const error = ref('')
const showCreateModal = ref(false)
const showTokenModal = ref(false)
const newToken = ref('')
const mutating = ref(false)
const mutationError = ref<string | null>(null)

const createForm = ref({
  name: '',
  description: '',
  role: ServiceAccountRole.MEMBER,
  expiryDays: 365,
})

const copied = ref(false)
let copyTimeout: ReturnType<typeof setTimeout> | null = null

function formatDate(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' })
}

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

onMounted(async () => {
  try {
    const resp = await adminClient().listServiceAccounts(
      create(ListServiceAccountsRequestSchema, {}),
    )
    serviceAccounts.value = resp.serviceAccounts
  } catch (e) {
    error.value = e instanceof ConnectError ? e.message : String(e)
  } finally {
    loading.value = false
  }
})

function openCreate() {
  createForm.value = {
    name: '',
    description: '',
    role: ServiceAccountRole.MEMBER,
    expiryDays: 365,
  }
  mutationError.value = null
  showCreateModal.value = true
}

function closeCreate() {
  showCreateModal.value = false
  mutationError.value = null
}

function closeTokenModal() {
  showTokenModal.value = false
  newToken.value = ''
}

async function submitCreate() {
  mutationError.value = null
  if (!createForm.value.name.trim()) {
    mutationError.value = 'Name is required.'
    return
  }
  mutating.value = true
  try {
    const resp = await adminClient().createServiceAccount(
      create(CreateServiceAccountRequestSchema, {
        name: createForm.value.name.trim(),
        description: createForm.value.description.trim(),
        role: createForm.value.role,
        expiryDays: createForm.value.expiryDays,
      }),
    )
    if (resp.serviceAccount) {
      serviceAccounts.value = [resp.serviceAccount, ...serviceAccounts.value]
    }
    newToken.value = resp.token
    showCreateModal.value = false
    showTokenModal.value = true
  } catch (e) {
    mutationError.value = e instanceof ConnectError ? e.message : String(e)
  } finally {
    mutating.value = false
  }
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

</script>

<template>
  <div class="space-y-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="font-display text-3xl font-bold tracking-tight text-on-surface">
          Service Accounts
        </h1>
        <p class="mt-2 max-w-2xl text-sm text-on-surface-variant">
          Machine identities for AI agents and automation. Create a service account and use its API token for programmatic access.
        </p>
      </div>
      <button
        type="button"
        class="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow hover:bg-primary/90"
        data-testid="sa-create-button"
        @click="openCreate"
      >
        <Plus class="size-4" />
        Create Service Account
      </button>
    </header>

    <!-- Workflow guide -->
    <div
      v-if="serviceAccounts.length > 0 && !loading"
      class="rounded-lg border border-outline-variant bg-surface-variant/20 p-5"
    >
      <h2 class="mb-3 font-display text-base font-semibold text-on-surface">
        Using Service Accounts
      </h2>
      <ol class="space-y-3 text-sm text-on-surface-variant">
        <li class="flex gap-3">
          <span
            class="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/15 text-xs font-bold text-primary"
            >1</span
          >
          <span
            ><strong class="text-on-surface">Create a service account</strong> — copy the token when
            prompted. Store it securely (e.g. in a secrets manager or CI/CD variable).</span
          >
        </li>
        <li class="flex gap-3">
          <span
            class="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/15 text-xs font-bold text-primary"
            >2</span
          >
          <span
            ><strong class="text-on-surface">Use the token</strong> — your AI agent or CLI tool can now
            authenticate with
            <code class="rounded bg-surface-variant px-1 py-0.5 font-mono text-xs"
              >Authorization: Bearer &lt;token&gt;</code
            >
            against the gateway.</span
          >
        </li>
      </ol>
    </div>

    <!-- Table -->
    <section
      class="overflow-hidden rounded-lg border border-outline-variant bg-surface"
      data-testid="sa-table"
    >
      <div
        v-if="loading"
        class="flex items-center justify-center gap-2 p-6 text-sm text-on-surface-variant"
      >
        <Loader2 class="size-4 animate-spin" />
        Loading service accounts…
      </div>
      <div v-else-if="error" class="p-4 text-sm text-error" data-testid="sa-error">
        Failed to load service accounts: {{ error }}
      </div>
      <div
        v-else-if="serviceAccounts.length === 0"
        class="space-y-4 p-6 text-center text-sm text-on-surface-variant"
        data-testid="sa-empty"
      >
        <div class="mx-auto flex size-12 items-center justify-center rounded-full bg-primary/15">
          <KeyRound class="size-6 text-primary" />
        </div>
        <div class="space-y-2">
          <p class="font-medium text-on-surface">No service accounts yet</p>
          <p class="mx-auto max-w-md">
            Service accounts are machine identities for AI agents, CI/CD pipelines, and CLI tools.
            They authenticate with long-lived API tokens instead of browser logins.
          </p>
        </div>
        <button
          type="button"
          class="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow hover:bg-primary/90"
          @click="openCreate"
        >
          <Plus class="size-4" />
          Create your first service account
        </button>
      </div>
      <table v-else class="w-full text-left text-sm">
        <thead class="bg-surface-variant/40 text-xs uppercase text-on-surface-variant">
          <tr>
            <th class="px-4 py-3">Name</th>
            <th class="px-4 py-3">Description</th>
            <th class="px-4 py-3">Role</th>
            <th class="px-4 py-3">Created</th>
            <th class="px-4 py-3 text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="sa in serviceAccounts"
            :key="sa.publicId"
            class="border-t border-outline-variant"
            :data-testid="`sa-row-${sa.publicId}`"
          >
            <td class="px-4 py-3">
              <div class="flex items-center gap-3">
                <div
                  class="flex size-9 items-center justify-center rounded-full bg-primary/15 text-xs font-semibold text-primary"
                  aria-hidden="true"
                >
                  <KeyRound class="size-4" />
                </div>
                <div class="min-w-0">
                  <router-link
                    :to="`${ROUTES.serviceAccountDetail.replace(':id', sa.publicId)}`"
                    class="truncate font-medium text-on-surface hover:text-primary hover:underline"
                  >
                    {{ sa.name }}
                  </router-link>
                  <div class="truncate text-xs text-on-surface-variant">{{ sa.publicId }}</div>
                </div>
              </div>
            </td>
            <td class="px-4 py-3 text-on-surface-variant">
              {{ sa.description || '—' }}
            </td>
            <td class="px-4 py-3">
              <span
                class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium"
                :class="roleClass(sa.role)"
              >
                {{ roleLabel(sa.role) }}
              </span>
            </td>
            <td class="px-4 py-3 text-on-surface-variant">
              {{ formatDate(sa.createdAt) }}
            </td>
            <td class="px-4 py-3">
              <div class="flex justify-end gap-1">
                <router-link
                  :to="`${ROUTES.serviceAccountDetail.replace(':id', sa.publicId)}`"
                  class="rounded p-1.5 text-sm font-medium text-primary hover:bg-primary/10"
                  :data-testid="`sa-manage-${sa.publicId}`"
                >
                  Manage
                </router-link>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- Create modal -->
    <Teleport to="body">
      <div
        v-if="showCreateModal"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
        @click.self="closeCreate"
      >
        <div
          class="w-full max-w-md rounded-lg bg-surface p-5 shadow-xl"
          role="dialog"
          aria-modal="true"
          data-testid="sa-create-modal"
        >
          <div class="mb-4 flex items-center justify-between">
            <h2 class="font-display text-lg font-semibold text-on-surface">
              Create Service Account
            </h2>
            <button
              type="button"
              class="rounded p-1 text-on-surface-variant hover:bg-surface-variant"
              @click="closeCreate"
            >
              <X class="size-4" />
            </button>
          </div>
          <form class="space-y-3" @submit.prevent="submitCreate">
            <label class="block text-sm">
              <span class="mb-1 block font-medium text-on-surface">
                Name <span class="text-error">*</span>
              </span>
              <input
                v-model="createForm.name"
                type="text"
                required
                data-testid="sa-create-name"
                class="w-full rounded-md border border-outline bg-surface px-3 py-2 text-on-surface placeholder:text-on-surface-variant focus:border-primary focus:outline-none"
                placeholder="e.g. ci-deploy"
              />
            </label>
            <label class="block text-sm">
              <span class="mb-1 block font-medium text-on-surface">Description</span>
              <input
                v-model="createForm.description"
                type="text"
                data-testid="sa-create-description"
                class="w-full rounded-md border border-outline bg-surface px-3 py-2 text-on-surface placeholder:text-on-surface-variant focus:border-primary focus:outline-none"
                placeholder="Optional description"
              />
            </label>
            <label class="block text-sm">
              <span class="mb-1 block font-medium text-on-surface">Role</span>
              <select
                v-model="createForm.role"
                data-testid="sa-create-role"
                class="w-full rounded-md border border-outline bg-surface px-3 py-2 text-on-surface focus:border-primary focus:outline-none"
              >
                <option :value="ServiceAccountRole.MEMBER">Member</option>
                <option :value="ServiceAccountRole.ADMIN">Admin</option>
              </select>
            </label>
            <label class="block text-sm">
              <span class="mb-1 block font-medium text-on-surface">Token Expiry (days)</span>
              <input
                v-model.number="createForm.expiryDays"
                type="number"
                min="0"
                data-testid="sa-create-expiry"
                class="w-full rounded-md border border-outline bg-surface px-3 py-2 text-on-surface focus:border-primary focus:outline-none"
              />
              <p class="mt-1 text-xs text-on-surface-variant">
                Number of days until the API token expires. 0 = no expiry.
              </p>
            </label>
            <p
              v-if="mutationError"
              class="rounded border border-error/40 bg-error/10 p-2 text-xs text-error"
              data-testid="sa-create-error"
            >
              {{ mutationError }}
            </p>
            <div class="mt-4 flex justify-end gap-2">
              <button
                type="button"
                class="rounded-md px-3 py-2 text-sm text-on-surface-variant hover:bg-surface-variant"
                @click="closeCreate"
              >
                Cancel
              </button>
              <button
                type="submit"
                :disabled="mutating"
                data-testid="sa-create-submit"
                class="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow hover:bg-primary/90 disabled:opacity-50"
              >
                <Loader2 v-if="mutating" class="size-4 animate-spin" />
                Create
              </button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Token display modal -->
    <Teleport to="body">
      <div
        v-if="showTokenModal"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
        @click.self="closeTokenModal"
      >
        <div
          class="w-full max-w-lg rounded-lg bg-surface p-5 shadow-xl"
          role="dialog"
          aria-modal="true"
          data-testid="sa-token-modal"
        >
          <div class="mb-4 flex items-center justify-between">
            <h2 class="font-display text-lg font-semibold text-on-surface">Token Created</h2>
            <button
              type="button"
              class="rounded p-1 text-on-surface-variant hover:bg-surface-variant"
              @click="closeTokenModal"
            >
              <X class="size-4" />
            </button>
          </div>
          <div class="space-y-4">
            <div
              class="rounded border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-amber-700 dark:text-amber-300"
            >
              Copy this token now. It won't be shown again.
            </div>
            <div class="space-y-2 text-sm text-on-surface-variant">
              <p>
                <strong class="text-on-surface">Next:</strong> Impersonate this account to set up
                MCP connections, then use the token with your AI agent or CLI tool:
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
                @click="closeTokenModal"
              >
                Done
              </button>
            </div>
          </div>
        </div>
      </div>
    </Teleport>


  </div>
</template>
