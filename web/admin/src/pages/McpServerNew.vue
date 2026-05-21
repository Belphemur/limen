<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ConnectError } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { ArrowLeft, Save } from '@lucide/vue'
import { ContextJsonEditor, hintsFor } from '@limen/shared'
import { adminClient, portalClient } from '@/transport/adminClient'
import { CreateUpstreamRequestSchema } from '@/gen/limen/admin/v1/admin_pb.ts'
import { ROUTES } from '@/router/routes'

interface Form {
  name: string
  displayName: string
  mcpUrl: string
  strategyType: 'none' | 'mcp_spec' | 'static_header'
  strategySubMode: 'tenant' | 'user' | ''
  // static_header tenant-mode shared API key.
  apiKey: string
  // static_header header template.
  headerName: string
  headerTemplate: string
  // mcp_spec optional static OAuth client override.
  oauthClientId: string
  oauthClientSecret: string
  // JSON object string.
  defaultsJson: string
}

const router = useRouter()
const form = reactive<Form>({
  name: '',
  displayName: '',
  mcpUrl: '',
  strategyType: 'none',
  strategySubMode: '',
  apiKey: '',
  headerName: 'Authorization',
  headerTemplate: 'Bearer {value}',
  oauthClientId: '',
  oauthClientSecret: '',
  defaultsJson: '',
})
const defaultsValid = ref(true)
const submitting = ref(false)
const error = ref<string | null>(null)

function onDefaultsValid(v: boolean) {
  defaultsValid.value = v
}

const hint = computed(() => hintsFor(form.mcpUrl))

// When the strategy or URL changes, sync the sub-mode default and
// re-prime defaults_json with the hint template (but only if the
// admin hasn't typed anything yet).
watch(
  () => form.strategyType,
  (s) => {
    if (s === 'static_header') {
      if (!form.strategySubMode) form.strategySubMode = 'tenant'
    } else {
      form.strategySubMode = ''
    }
  },
)

watch(hint, (h) => {
  if (!h) return
  if (form.defaultsJson.trim() !== '') return
  form.defaultsJson = JSON.stringify(h.template, null, 2)
})

const canSubmit = computed(
  () =>
    !submitting.value &&
    defaultsValid.value &&
    form.name.trim() !== '' &&
    form.mcpUrl.trim() !== '' &&
    (form.strategyType !== 'static_header' ||
      (form.headerName.trim() !== '' && form.headerTemplate.trim() !== '')),
)

function buildStrategyConfig(): Record<string, string> {
  if (form.strategyType === 'static_header') {
    const cfg: Record<string, string> = {
      header_name: form.headerName,
      header_template: form.headerTemplate,
      mode: form.strategySubMode || 'tenant',
    }
    if (form.strategySubMode === 'tenant' && form.apiKey) cfg.value = form.apiKey
    return cfg
  }
  return {}
}

async function submit() {
  if (!canSubmit.value) return
  submitting.value = true
  error.value = null
  try {
    const resp = await adminClient().createUpstream(
      create(CreateUpstreamRequestSchema, {
        name: form.name.trim(),
        displayName: form.displayName.trim(),
        mcpUrl: form.mcpUrl.trim(),
        strategyType: form.strategyType,
        strategySubMode: form.strategySubMode,
        strategyConfig: buildStrategyConfig(),
        defaultsJson: form.defaultsJson.trim(),
        oauthClientOverride:
          form.strategyType === 'mcp_spec' && form.oauthClientId.trim() !== ''
            ? {
                $typeName: 'limen.admin.v1.OAuthClientOverride',
                clientId: form.oauthClientId.trim(),
                clientSecret: form.oauthClientSecret,
              }
            : undefined,
      }),
    )

    if (resp.requiresAdminLink) {
      try {
        const sc = await portalClient().startConnect({
          upstreamName: form.name.trim(),
          returnTo: ROUTES.mcpServers,
        })
        if (sc.redirectUrl) {
          window.location.href = sc.redirectUrl
          return
        }
      } catch (sErr) {
        // Created OK but startConnect failed — fall through to the list.
        console.warn('startConnect after create failed', sErr)
      }
    }
    await router.push(ROUTES.mcpServers)
  } catch (err) {
    if (err instanceof ConnectError) {
      error.value = err.rawMessage || err.message
    } else {
      error.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    submitting.value = false
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
        Add MCP server
      </h1>
    </header>

    <form class="space-y-stack-md" data-testid="upstream-new-form" @submit.prevent="submit">
      <div
        v-if="error"
        role="alert"
        class="rounded-md border border-error bg-error/10 px-3 py-2 text-sm text-error"
        data-testid="upstream-new-error"
      >
        {{ error }}
      </div>

      <div class="grid gap-stack-md md:grid-cols-2">
        <label class="block">
          <span class="text-sm font-medium text-on-surface">Name</span>
          <input
            v-model="form.name"
            type="text"
            required
            pattern="[a-z0-9_]+"
            class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            data-testid="field-name"
          />
          <span class="mt-1 block text-xs text-on-surface-variant">
            Stable identifier (lowercase, underscores). Used in tool prefixes.
          </span>
        </label>

        <label class="block">
          <span class="text-sm font-medium text-on-surface">Display name</span>
          <input
            v-model="form.displayName"
            type="text"
            class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            data-testid="field-display-name"
          />
          <span class="mt-1 block text-xs text-on-surface-variant">
            Optional — falls back to the name.
          </span>
        </label>
      </div>

      <label class="block">
        <span class="text-sm font-medium text-on-surface">MCP URL</span>
        <input
          v-model="form.mcpUrl"
          type="url"
          required
          placeholder="https://example.com/mcp"
          class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
          data-testid="field-mcp-url"
        />
      </label>

      <fieldset class="space-y-2">
        <legend class="text-sm font-medium text-on-surface">Strategy</legend>
        <div class="flex flex-wrap gap-4">
          <label class="inline-flex items-center gap-2 text-sm">
            <input
              v-model="form.strategyType"
              type="radio"
              value="none"
              data-testid="strategy-none"
            />
            None (no auth)
          </label>
          <label class="inline-flex items-center gap-2 text-sm">
            <input
              v-model="form.strategyType"
              type="radio"
              value="mcp_spec"
              data-testid="strategy-mcp-spec"
            />
            mcp_spec (OAuth DCR)
          </label>
          <label class="inline-flex items-center gap-2 text-sm">
            <input
              v-model="form.strategyType"
              type="radio"
              value="static_header"
              data-testid="strategy-static-header"
            />
            static_header
          </label>
        </div>
      </fieldset>

      <div v-if="form.strategyType === 'static_header'" class="space-y-stack-md rounded-md border border-border-subtle bg-surface-container-low p-4">
        <fieldset class="space-y-2">
          <legend class="text-sm font-medium text-on-surface">Mode</legend>
          <div class="flex gap-4">
            <label class="inline-flex items-center gap-2 text-sm">
              <input v-model="form.strategySubMode" type="radio" value="tenant" />
              tenant (shared key)
            </label>
            <label class="inline-flex items-center gap-2 text-sm">
              <input v-model="form.strategySubMode" type="radio" value="user" />
              user (each member supplies their own)
            </label>
          </div>
        </fieldset>
        <div class="grid gap-stack-md md:grid-cols-2">
          <label class="block">
            <span class="text-sm font-medium text-on-surface">Header name</span>
            <input
              v-model="form.headerName"
              type="text"
              class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            />
          </label>
          <label class="block">
            <span class="text-sm font-medium text-on-surface">Header template</span>
            <input
              v-model="form.headerTemplate"
              type="text"
              class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            />
            <span class="mt-1 block text-xs text-on-surface-variant">
              Use <code class="text-xs">{value}</code> as the substitution token.
            </span>
          </label>
        </div>
        <label v-if="form.strategySubMode === 'tenant'" class="block">
          <span class="text-sm font-medium text-on-surface">Shared API key</span>
          <input
            v-model="form.apiKey"
            type="password"
            class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            data-testid="field-api-key"
          />
        </label>
      </div>

      <div v-if="form.strategyType === 'mcp_spec'" class="space-y-stack-md rounded-md border border-border-subtle bg-surface-container-low p-4">
        <p class="text-sm text-on-surface-variant">
          Optional: pre-registered OAuth client for upstreams that don't support DCR.
          Leave blank to use dynamic client registration.
        </p>
        <div class="grid gap-stack-md md:grid-cols-2">
          <label class="block">
            <span class="text-sm font-medium text-on-surface">Client ID</span>
            <input
              v-model="form.oauthClientId"
              type="text"
              class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            />
          </label>
          <label class="block">
            <span class="text-sm font-medium text-on-surface">Client secret</span>
            <input
              v-model="form.oauthClientSecret"
              type="password"
              class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
            />
          </label>
        </div>
      </div>

      <div>
        <label class="mb-1 block text-sm font-medium text-on-surface">
          Defaults JSON (context blob)
        </label>
        <ContextJsonEditor
          v-model="form.defaultsJson"
          :caption="hint?.caption"
          @update:valid="onDefaultsValid"
        />
      </div>

      <div class="flex items-center gap-2">
        <button
          type="submit"
          :disabled="!canSubmit"
          class="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow-sm hover:bg-primary-container disabled:opacity-50"
          data-testid="submit-upstream"
        >
          <Save :size="16" aria-hidden="true" />
          {{ submitting ? 'Creating…' : 'Create server' }}
        </button>
      </div>
    </form>
  </div>
</template>
