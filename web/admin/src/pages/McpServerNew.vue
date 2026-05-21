<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ConnectError } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { ArrowLeft, KeyRound, ShieldCheck, ShieldOff, Save } from '@lucide/vue'
import { ContextJsonEditor, hintsFor } from '@limen/shared'
import { adminClient, portalClient } from '@/transport/adminClient'
import { CreateUpstreamRequestSchema } from '@/gen/limen/admin/v1/admin_pb.ts'
import { ROUTES } from '@/router/routes'

type StrategyType = 'none' | 'mcp_spec' | 'static_header'

interface Form {
  displayName: string
  name: string
  // True until the admin manually edits the identifier — after that
  // we stop overwriting it from the display name.
  nameAutoDerived: boolean
  mcpUrl: string
  strategyType: StrategyType
  strategySubMode: 'tenant' | 'user' | ''
  apiKey: string
  headerName: string
  headerTemplate: string
  oauthClientId: string
  oauthClientSecret: string
  defaultsJson: string
}

const router = useRouter()
const form = reactive<Form>({
  displayName: '',
  name: '',
  nameAutoDerived: true,
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
const existingNames = ref<Set<string>>(new Set())

function onDefaultsValid(v: boolean) {
  defaultsValid.value = v
}

// Lowercase, replace non-alnum with underscore, collapse repeats, trim.
function slugify(input: string): string {
  return input
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/_+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 64)
}

watch(
  () => form.displayName,
  (dn) => {
    if (!form.nameAutoDerived) return
    form.name = slugify(dn)
  },
)

function onNameInput(ev: Event) {
  form.name = (ev.target as HTMLInputElement).value
  form.nameAutoDerived = false
}

function resetNameDerivation() {
  form.nameAutoDerived = true
  form.name = slugify(form.displayName)
}

onMounted(async () => {
  try {
    const resp = await portalClient().listUpstreams({})
    existingNames.value = new Set(resp.upstreams.map((u) => u.name))
  } catch (err) {
    // Soft-fail: backend create still enforces uniqueness, so the
    // pre-check is purely advisory.
    console.warn('listUpstreams (uniqueness check) failed', err)
  }
})

const hint = computed(() => hintsFor(form.mcpUrl))

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

const nameError = computed<string | null>(() => {
  const n = form.name.trim()
  if (n === '') return 'Identifier required'
  if (!/^[a-z0-9_]+$/.test(n)) return 'Lowercase letters, digits and underscores only'
  if (existingNames.value.has(n)) return 'Identifier already in use'
  return null
})

const canSubmit = computed(
  () =>
    !submitting.value &&
    defaultsValid.value &&
    nameError.value === null &&
    form.displayName.trim() !== '' &&
    form.mcpUrl.trim() !== '' &&
    (form.strategyType !== 'static_header' ||
      (form.headerName.trim() !== '' && form.headerTemplate.trim() !== '')),
)

interface StrategyCard {
  value: StrategyType
  label: string
  description: string
  testid: string
}

const strategies: StrategyCard[] = [
  {
    value: 'none',
    label: 'None',
    description: 'For internal network deployments or fully public open servers.',
    testid: 'strategy-none',
  },
  {
    value: 'static_header',
    label: 'Static Header',
    description: 'Send a fixed API key or secret in a designated HTTP header.',
    testid: 'strategy-static-header',
  },
  {
    value: 'mcp_spec',
    label: 'MCP Spec (OAuth)',
    description: 'Standard OAuth/PKCE flow for strictly MCP-compliant servers.',
    testid: 'strategy-mcp-spec',
  },
]

function strategyIconComponent(s: StrategyType) {
  switch (s) {
    case 'static_header':
      return KeyRound
    case 'mcp_spec':
      return ShieldCheck
    default:
      return ShieldOff
  }
}

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
    <div>
      <button
        type="button"
        class="group inline-flex items-center gap-1 rounded px-1 py-1 text-sm text-on-surface-variant hover:text-primary"
        @click="router.push(ROUTES.mcpServers)"
      >
        <ArrowLeft
          :size="16"
          aria-hidden="true"
          class="transition-transform group-hover:-translate-x-0.5"
        />
        Back to servers
      </button>
      <h1 class="mt-stack-sm font-display text-2xl font-bold tracking-tight text-on-surface">
        Add MCP server
      </h1>
      <p class="mt-1 max-w-2xl text-sm text-on-surface-variant">
        Connect a new Model Context Protocol (MCP) server. We'll probe it with the
        chosen authentication strategy before the configuration is saved.
      </p>
    </div>

    <form class="space-y-stack-lg" data-testid="upstream-new-form" @submit.prevent="submit">
      <div
        v-if="error"
        role="alert"
        class="rounded-md border border-error bg-error/10 px-3 py-2 text-sm text-error"
        data-testid="upstream-new-error"
      >
        {{ error }}
      </div>

      <section class="rounded-xl border border-outline-variant bg-surface-container-lowest">
        <header class="border-b border-outline-variant px-4 py-3">
          <h2 class="text-base font-semibold text-on-surface">Basic information</h2>
        </header>
        <div class="grid gap-stack-md p-4 md:grid-cols-2">
          <label class="block md:col-span-2">
            <span class="text-sm font-medium text-on-surface">Display name</span>
            <input
              v-model="form.displayName"
              type="text"
              required
              placeholder="e.g. Internal Data Vectorizer"
              class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
              data-testid="field-display-name"
            />
            <span class="mt-1 block text-xs text-on-surface-variant">
              Shown in the catalog and to end users.
            </span>
          </label>

          <label class="block md:col-span-2">
            <span class="flex items-center justify-between text-sm font-medium text-on-surface">
              Identifier
              <button
                v-if="!form.nameAutoDerived"
                type="button"
                class="text-xs font-normal text-primary hover:underline"
                @click="resetNameDerivation"
              >
                Re-derive from display name
              </button>
            </span>
            <input
              :value="form.name"
              type="text"
              required
              pattern="[a-z0-9_]+"
              placeholder="auto"
              class="mt-1 block w-full rounded-md border bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:outline-none focus:ring-1"
              :class="
                nameError && form.name !== ''
                  ? 'border-error focus:border-error focus:ring-error'
                  : 'border-outline-variant focus:border-primary focus:ring-primary'
              "
              data-testid="field-name"
              @input="onNameInput"
            />
            <span
              v-if="nameError && form.name !== ''"
              class="mt-1 block text-xs text-error"
              data-testid="field-name-error"
            >
              {{ nameError }}
            </span>
            <span v-else class="mt-1 block text-xs text-on-surface-variant">
              Stable identifier used in tool prefixes. Auto-derived from the display
              name; edit only if you need a specific slug.
            </span>
          </label>

          <label class="block md:col-span-2">
            <span class="text-sm font-medium text-on-surface">MCP server URL</span>
            <input
              v-model="form.mcpUrl"
              type="url"
              required
              placeholder="https://api.internal.corp/mcp/v1"
              class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
              data-testid="field-mcp-url"
            />
          </label>
        </div>
      </section>

      <section class="space-y-stack-sm">
        <h2 class="text-base font-semibold text-on-surface">Authentication strategy</h2>
        <div class="grid gap-stack-md md:grid-cols-3" role="radiogroup">
          <label
            v-for="opt in strategies"
            :key="opt.value"
            class="relative flex h-full cursor-pointer flex-col gap-stack-sm rounded-xl border bg-surface-container-lowest p-4 transition-colors"
            :class="
              form.strategyType === opt.value
                ? 'border-primary ring-1 ring-primary'
                : 'border-outline-variant hover:border-outline'
            "
          >
            <input
              v-model="form.strategyType"
              type="radio"
              :value="opt.value"
              class="sr-only"
              :data-testid="opt.testid"
            />
            <span
              class="flex h-9 w-9 items-center justify-center rounded-full"
              :class="
                form.strategyType === opt.value
                  ? 'bg-primary-container text-primary'
                  : 'bg-surface-container text-on-surface-variant'
              "
            >
              <component :is="strategyIconComponent(opt.value)" :size="18" aria-hidden="true" />
            </span>
            <div>
              <h3 class="text-sm font-semibold text-on-surface">{{ opt.label }}</h3>
              <p class="mt-1 text-xs text-on-surface-variant">{{ opt.description }}</p>
            </div>
          </label>
        </div>
      </section>

      <section
        v-if="form.strategyType === 'static_header'"
        class="space-y-stack-md rounded-xl border border-primary/30 bg-surface-container-lowest ring-1 ring-primary/10"
      >
        <header class="border-b border-outline-variant px-4 py-3">
          <h2 class="text-base font-semibold text-on-surface">Header configuration</h2>
        </header>
        <div class="space-y-stack-md p-4">
          <fieldset class="space-y-2">
            <legend class="text-sm font-medium text-on-surface">Secret resolution mode</legend>
            <div class="inline-flex rounded-md border border-outline-variant bg-surface-container p-1">
              <button
                type="button"
                class="rounded px-3 py-1 text-xs font-medium transition-colors"
                :class="
                  form.strategySubMode !== 'user'
                    ? 'bg-surface-container-lowest text-primary shadow-sm'
                    : 'text-on-surface-variant'
                "
                @click="form.strategySubMode = 'tenant'"
              >
                Tenant (shared)
              </button>
              <button
                type="button"
                class="rounded px-3 py-1 text-xs font-medium transition-colors"
                :class="
                  form.strategySubMode === 'user'
                    ? 'bg-surface-container-lowest text-primary shadow-sm'
                    : 'text-on-surface-variant'
                "
                @click="form.strategySubMode = 'user'"
              >
                User (individual)
              </button>
            </div>
            <p class="text-xs text-on-surface-variant">
              Tenant mode uses one global secret. User mode lets each member supply
              their own.
            </p>
          </fieldset>
          <div class="grid gap-stack-md md:grid-cols-2">
            <label class="block">
              <span class="text-sm font-medium text-on-surface">Header name</span>
              <input
                v-model="form.headerName"
                type="text"
                class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
              />
            </label>
            <label class="block">
              <span class="text-sm font-medium text-on-surface">Header value template</span>
              <input
                v-model="form.headerTemplate"
                type="text"
                class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
              />
              <span class="mt-1 block text-xs text-on-surface-variant">
                Use <code class="font-mono">{value}</code> as the substitution token.
              </span>
            </label>
          </div>
          <label v-if="form.strategySubMode === 'tenant'" class="block border-t border-dashed border-outline-variant pt-stack-md">
            <span class="flex items-center justify-between text-sm font-medium text-on-surface">
              Shared tenant secret
              <span class="text-xs font-normal text-on-surface-variant">Required</span>
            </span>
            <input
              v-model="form.apiKey"
              type="password"
              placeholder="Enter secure token or API key"
              class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
              data-testid="field-api-key"
            />
          </label>
        </div>
      </section>

      <section
        v-if="form.strategyType === 'mcp_spec'"
        class="space-y-stack-md rounded-xl border border-outline-variant bg-surface-container-lowest"
      >
        <header class="border-b border-outline-variant px-4 py-3">
          <h2 class="text-base font-semibold text-on-surface">Static OAuth client (optional)</h2>
        </header>
        <div class="space-y-stack-md p-4">
          <p class="text-xs text-on-surface-variant">
            Override Dynamic Client Registration with a pre-registered OAuth client
            for upstreams that don't support DCR. Leave blank to use DCR.
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
      </section>

      <section class="rounded-xl border border-outline-variant bg-surface-container-lowest">
        <header class="border-b border-outline-variant px-4 py-3">
          <h2 class="text-base font-semibold text-on-surface">Defaults JSON (context blob)</h2>
        </header>
        <div class="p-4">
          <ContextJsonEditor
            v-model="form.defaultsJson"
            :caption="hint?.caption"
            @update:valid="onDefaultsValid"
          />
        </div>
      </section>

      <div class="flex items-center justify-end gap-3">
        <button
          type="button"
          class="rounded-md border border-outline-variant px-3 py-2 text-sm text-on-surface hover:bg-surface-container-low"
          @click="router.push(ROUTES.mcpServers)"
        >
          Cancel
        </button>
        <button
          type="submit"
          :disabled="!canSubmit"
          class="inline-flex items-center gap-1.5 rounded-md bg-primary px-4 py-2 text-sm font-medium text-on-primary shadow-sm hover:bg-primary-container disabled:opacity-50"
          data-testid="submit-upstream"
        >
          <Save :size="16" aria-hidden="true" />
          {{ submitting ? 'Testing & saving…' : 'Test & save server' }}
        </button>
      </div>
    </form>
  </div>
</template>
