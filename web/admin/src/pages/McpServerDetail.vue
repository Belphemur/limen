<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ConnectError, Code } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { ArrowLeft, RefreshCw, Trash2, Save, ChevronDown } from '@lucide/vue'
import { ContextJsonEditor, hintsFor } from '@limen/shared'
import { adminClient, portalClient } from '@/transport/adminClient'
import {
  DeleteUpstreamRequestSchema,
  ReindexUpstreamCatalogRequestSchema,
  UpdateUpstreamRequestSchema,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import type { UpstreamSummary } from '@/gen/limen/portal/v1/portal_pb.ts'
import { ROUTES } from '@/router/routes'

const route = useRoute()
const router = useRouter()
const publicId = computed(() => String(route.params.id))

const summary = ref<UpstreamSummary | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)

const displayName = ref('')
const defaultsJson = ref('')
const defaultsValid = ref(true)
const saving = ref(false)

function onDefaultsValid(v: boolean) {
  defaultsValid.value = v
}
const reindexing = ref(false)
const deleting = ref(false)
const confirmText = ref('')
const toolsOpen = ref(false)

const defaultsHint = computed(() => (summary.value ? hintsFor(summary.value.mcpUrl) : null))

async function refresh() {
  loading.value = true
  error.value = null
  try {
    const resp = await portalClient().listUpstreams({})
    const found = resp.upstreams.find((u) => u.publicId === publicId.value) ?? null
    summary.value = found
    if (found) {
      displayName.value = found.displayName
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

onMounted(refresh)

const canSave = computed(
  () =>
    !saving.value &&
    defaultsValid.value &&
    summary.value !== null &&
    (displayName.value !== summary.value.displayName || defaultsJson.value.trim() !== ''),
)

async function save() {
  if (!canSave.value || !summary.value) return
  saving.value = true
  error.value = null
  try {
    const resp = await adminClient().updateUpstream(
      create(UpdateUpstreamRequestSchema, {
        publicId: publicId.value,
        displayName: displayName.value,
        defaultsJson: defaultsJson.value.trim(),
      }),
    )
    if (resp.upstream) {
      summary.value = resp.upstream
      displayName.value = resp.upstream.displayName
      defaultsJson.value = ''
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    saving.value = false
  }
}

async function reindex() {
  if (!summary.value) return
  reindexing.value = true
  error.value = null
  try {
    const resp = await adminClient().reindexUpstreamCatalog(
      create(ReindexUpstreamCatalogRequestSchema, { publicId: publicId.value }),
    )
    if (resp.upstream) summary.value = resp.upstream
  } catch (err) {
    const code = err instanceof ConnectError ? err.code : null
    if (code === Code.FailedPrecondition) {
      error.value = 'Reindex requires a link. Connect this upstream first.'
    } else {
      error.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    reindexing.value = false
  }
}

async function remove() {
  if (!summary.value) return
  if (confirmText.value !== summary.value.identifier) {
    error.value = `Type "${summary.value.identifier}" to confirm deletion.`
    return
  }
  deleting.value = true
  error.value = null
  try {
    await adminClient().deleteUpstream(
      create(DeleteUpstreamRequestSchema, { publicId: publicId.value }),
    )
    await router.push(ROUTES.mcpServers)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="space-y-stack-lg">
    <header class="flex items-center gap-3">
      <button
        type="button"
        class="inline-flex items-center gap-1 rounded px-2 py-1 text-sm text-on-surface-variant hover:bg-surface-container-low hover:text-on-surface"
        @click="router.push(ROUTES.mcpServers)"
      >
        <ArrowLeft :size="16" aria-hidden="true" />
        Back
      </button>
      <h1 class="font-display text-2xl font-bold tracking-tight text-on-surface">
        {{ summary?.displayName || summary?.identifier || 'MCP server' }}
      </h1>
    </header>

    <div
      v-if="error"
      role="alert"
      class="rounded-md border border-error bg-error/10 px-3 py-2 text-sm text-error"
      data-testid="upstream-detail-error"
    >
      {{ error }}
    </div>

    <section v-if="loading" class="text-sm text-on-surface-variant">Loading…</section>

    <template v-else-if="summary">
      <section class="rounded-lg border border-border-subtle bg-surface p-4">
        <dl class="grid gap-stack-sm md:grid-cols-2 text-sm">
          <div>
            <dt class="text-on-surface-variant">Identifier</dt>
            <dd class="font-mono text-on-surface">{{ summary.identifier }}</dd>
          </div>
          <div>
            <dt class="text-on-surface-variant">MCP URL</dt>
            <dd class="font-mono text-on-surface">{{ summary.mcpUrl }}</dd>
          </div>
          <div>
            <dt class="text-on-surface-variant">Strategy</dt>
            <dd class="text-on-surface">
              {{ summary.strategyType }}<span v-if="summary.strategySubMode">
                · {{ summary.strategySubMode }}</span
              >
            </dd>
          </div>
          <div>
            <dt class="text-on-surface-variant">Tools cached</dt>
            <dd class="text-on-surface">{{ summary.tools.length }}</dd>
          </div>
          <div class="md:col-span-2">
            <dt class="text-on-surface-variant">Aliases</dt>
            <dd class="mt-1 flex flex-wrap gap-1" data-testid="upstream-aliases">
              <span
                v-for="alias in summary.aliases"
                :key="alias"
                class="inline-flex items-center rounded-full border border-surface-dim bg-surface-container-low px-2 py-0.5 font-mono text-xs text-primary"
              >
                {{ alias }}
              </span>
              <span v-if="summary.aliases.length === 0" class="text-on-surface-variant">
                None
              </span>
            </dd>
          </div>
        </dl>
      </section>

      <section class="rounded-lg border border-border-subtle bg-surface">
        <button
          type="button"
          class="flex w-full items-center justify-between gap-2 px-4 py-3 text-left"
          :aria-expanded="toolsOpen"
          data-testid="tools-disclosure"
          @click="toolsOpen = !toolsOpen"
        >
          <span class="font-display text-lg font-semibold text-on-surface">
            Tools
            <span class="ml-1 text-sm font-normal text-on-surface-variant">
              ({{ summary.tools.length }})
            </span>
          </span>
          <ChevronDown
            :size="18"
            class="text-on-surface-variant transition-transform"
            :class="toolsOpen ? 'rotate-180' : ''"
            aria-hidden="true"
          />
        </button>
        <div v-if="toolsOpen" class="border-t border-border-subtle px-4 py-3">
          <p v-if="summary.tools.length === 0" class="text-sm text-on-surface-variant">
            No tools cached yet. Reindex the catalog after the upstream is linked.
          </p>
          <ul v-else class="divide-y divide-border-subtle" data-testid="tools-list">
            <li v-for="tool in summary.tools" :key="tool.name" class="py-2">
              <div class="font-mono text-sm text-on-surface">{{ tool.name }}</div>
              <div
                v-if="tool.description"
                class="mt-0.5 text-sm text-on-surface-variant"
              >
                {{ tool.description }}
              </div>
            </li>
          </ul>
        </div>
      </section>

      <section class="space-y-stack-md rounded-lg border border-border-subtle bg-surface p-4">
        <h2 class="font-display text-lg font-semibold text-on-surface">Edit</h2>
        <label class="block">
          <span class="text-sm font-medium text-on-surface">Display name</span>
          <input
            v-model="displayName"
            type="text"
            class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            data-testid="field-display-name"
          />
        </label>
        <div>
          <label class="mb-1 block text-sm font-medium text-on-surface">
            Ambient context (optional)
          </label>
          <p class="mb-2 text-xs text-on-surface-variant">
            Pre-filled values the LLM can use without asking the user — Atlassian
            <code class="font-mono">cloudId</code>, Sentry <code class="font-mono">organization_slug</code>,
            Cloudflare <code class="font-mono">account_id</code>, default project keys, region names,
            and other stable identifiers this MCP server expects on most tool calls. Provide a JSON
            object whose keys are merged into every tool call's arguments as defaults; tool calls
            may still override any field. Leave blank to keep the current value.
          </p>
          <ContextJsonEditor
            v-model="defaultsJson"
            :caption="defaultsHint?.caption"
            @update:valid="onDefaultsValid"
          />
        </div>
        <button
          type="button"
          :disabled="!canSave"
          class="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow-sm hover:bg-primary/90 disabled:opacity-50"
          data-testid="save-upstream"
          @click="save"
        >
          <Save :size="16" aria-hidden="true" />
          {{ saving ? 'Saving…' : 'Save changes' }}
        </button>
      </section>

      <section class="space-y-stack-md rounded-lg border border-border-subtle bg-surface p-4">
        <h2 class="font-display text-lg font-semibold text-on-surface">Catalog</h2>
        <p class="text-sm text-on-surface-variant">
          Re-fetch the tool list from the upstream. Required after upstream-side changes.
        </p>
        <button
          type="button"
          :disabled="reindexing"
          class="inline-flex items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 py-2 text-sm font-medium text-on-surface hover:bg-surface-container-low disabled:opacity-50"
          data-testid="reindex-upstream"
          @click="reindex"
        >
          <RefreshCw :size="16" aria-hidden="true" />
          {{ reindexing ? 'Reindexing…' : 'Reindex catalog' }}
        </button>
        <div>
          <h3 class="text-sm font-medium text-on-surface-variant">
            Preview merged context (coming soon)
          </h3>
          <button
            type="button"
            disabled
            class="mt-1 inline-flex items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 py-2 text-sm font-medium text-on-surface-variant opacity-50"
          >
            Preview
          </button>
        </div>
      </section>

      <section class="space-y-stack-md rounded-lg border border-error/40 bg-error/5 p-4">
        <h2 class="font-display text-lg font-semibold text-error">Danger zone</h2>
        <p class="text-sm text-on-surface-variant">
          Type the upstream identifier <code class="font-mono text-on-surface">{{ summary.identifier }}</code> to enable deletion.
        </p>
        <input
          v-model="confirmText"
          type="text"
          class="block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
          data-testid="delete-confirm"
        />
        <button
          type="button"
          :disabled="deleting || confirmText !== summary.identifier"
          class="inline-flex items-center gap-1.5 rounded-md bg-error px-3 py-2 text-sm font-medium text-on-error shadow-sm hover:bg-error/90 disabled:opacity-50"
          data-testid="delete-upstream"
          @click="remove"
        >
          <Trash2 :size="16" aria-hidden="true" />
          {{ deleting ? 'Deleting…' : 'Delete upstream' }}
        </button>
      </section>
    </template>

    <section v-else class="text-sm text-on-surface-variant">Upstream not found.</section>
  </div>
</template>
