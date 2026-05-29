<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ConnectError } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { StructSchema } from '@bufbuild/protobuf/wkt'
import { ArrowLeft, ChevronDown, KeyRound, ShieldCheck, ShieldOff, Save } from '@lucide/vue'
import {
  ContextJsonEditor,
  ErrorModal,
  SuccessModal,
  hintsFor,
  openOAuthPopup,
} from '@limen/shared'
import { tenantPrefix } from '@limen/shared/session'
import { adminClient, portalClient } from '@/transport/adminClient'
import {
  CreateUpstreamRequestSchema,
  DeleteUpstreamRequestSchema,
  StaticHeaderMode,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import { ROUTES } from '@/router/routes'

type StrategyType = 'none' | 'mcp_spec' | 'static_header'

interface Form {
  displayName: string
  identifier: string
  // True until the admin manually edits the identifier — after that
  // we stop overwriting it from the display name.
  identifierAutoDerived: boolean
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
  identifier: '',
  identifierAutoDerived: true,
  mcpUrl: '',
  strategyType: 'mcp_spec',
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
const existingNames = ref<Set<string>>(new Set())
const staticClientOpen = ref(false)
const defaultsOpen = ref(false)

interface ErrorState {
  title: string
  message: string
  // Optional retry handler — when set the modal exposes a primary button.
  retry?: () => void
}
const errorState = ref<ErrorState | null>(null)
const successOpen = ref(false)
const successUpstream = ref<{ name: string; publicId: string } | null>(null)

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
    if (!form.identifierAutoDerived) return
    form.identifier = slugify(dn)
  },
)

function onNameInput(ev: Event) {
  form.identifier = (ev.target as HTMLInputElement).value
  form.identifierAutoDerived = false
}

function resetNameDerivation() {
  form.identifierAutoDerived = true
  form.identifier = slugify(form.displayName)
}

onMounted(async () => {
  try {
    const resp = await portalClient().listUpstreams({})
    existingNames.value = new Set(resp.upstreams.map((u) => u.identifier))
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
  defaultsOpen.value = true
})

const nameError = computed<string | null>(() => {
  const n = form.identifier.trim()
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
    value: 'mcp_spec',
    label: 'MCP Spec (OAuth)',
    description: 'Standard OAuth/PKCE flow for strictly MCP-compliant servers.',
    testid: 'strategy-mcp-spec',
  },
  {
    value: 'static_header',
    label: 'Static Header',
    description: 'Send a fixed API key or secret in a designated HTTP header.',
    testid: 'strategy-static-header',
  },
  {
    value: 'none',
    label: 'None',
    description: 'For internal network deployments or fully public open servers.',
    testid: 'strategy-none',
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
    }
    if (form.apiKey) cfg.value = form.apiKey
    return cfg
  }
  return {}
}

function describeError(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message
  if (err instanceof Error) return err.message
  return String(err)
}

// Extracts the `{stage, strategy, reason}` detail attached by the
// backend's mapProvisionError. Returns null when the error is not a
// structured provision failure.
function extractProvisionDetail(
  err: unknown,
): { stage: string; reason: string; strategy?: string } | null {
  if (!(err instanceof ConnectError)) return null
  const structs = err.findDetails(StructSchema)
  for (const s of structs) {
    const stage = s.fields['stage']?.kind.case === 'stringValue' ? s.fields['stage'].kind.value : ''
    if (!stage) continue
    const reason =
      s.fields['reason']?.kind.case === 'stringValue'
        ? s.fields['reason'].kind.value
        : (err.rawMessage ?? '')
    const strategy =
      s.fields['strategy']?.kind.case === 'stringValue'
        ? s.fields['strategy'].kind.value
        : undefined
    return { stage, reason, strategy }
  }
  return null
}

function provisionErrorState(err: unknown): ErrorState {
  const detail = extractProvisionDetail(err)
  if (!detail) {
    return { title: 'Connection Failed', message: describeError(err) }
  }
  switch (detail.stage) {
    case 'discovery':
      return {
        title: 'Could not reach the MCP server',
        message: `We could not discover the OAuth metadata for this server. Check that the URL is reachable from Limen and exposes either a Protected Resource Metadata document or that you provided the AS issuer/endpoints. (${detail.reason})`,
      }
    case 'dcr':
      return {
        title: 'Dynamic client registration rejected',
        message: `The authorization server refused to register Limen as an OAuth client. (${detail.reason})`,
      }
    case 'static_client_required':
      return {
        title: 'Static OAuth client required',
        message: `This authorization server does not support dynamic client registration. Provide an OAuth client ID and secret in the advanced section and try again. (${detail.reason})`,
      }
    case 'persist':
      return {
        title: 'Could not save provisioning result',
        message: `Provisioning succeeded but we failed to store the result. (${detail.reason})`,
      }
    default:
      return {
        title: 'Provisioning failed',
        message: detail.reason || describeError(err),
      }
  }
}

async function rollbackUpstream(publicId: string) {
  try {
    await adminClient().deleteUpstream(create(DeleteUpstreamRequestSchema, { publicId }))
  } catch (delErr) {
    // Surface as a console warning — the modal still shows the
    // original OAuth failure; the admin can clean up from the list.
    console.warn('rollback deleteUpstream failed', delErr)
  }
}

async function runOAuthPopup(upstreamIdentifier: string, publicId: string) {
  // Anchor returnTo on the SPA's own origin so the upstream callback
  // (which may live on a different host if base_url is misconfigured
  // or sits behind a separate reverse-proxy) bounces the popup back
  // to the same origin as this opener — required for the
  // BroadcastChannel/postMessage handshake in openOAuthPopup.
  const prefix = tenantPrefix() ?? ''
  const adminBase = window.location.pathname.startsWith(`${prefix}/admin/`)
    ? `${prefix}/admin`
    : prefix
  const sc = await portalClient().startConnect({
    upstreamIdentifier,
    returnTo: `${window.location.origin}${adminBase}${ROUTES.oauthPopupClose}`,
  })
  if (!sc.redirectUrl) {
    throw new Error('Backend did not return an authorize URL')
  }
  const result = await openOAuthPopup({ url: sc.redirectUrl })
  console.log('[McpServerNew] openOAuthPopup result:', result)
  if (!result.ok) {
    console.log(
      `[McpServerNew] OAuth failed branch: error=${result.error}, calling rollbackUpstream for publicId=${publicId}`,
    )
    await rollbackUpstream(publicId)
    const message =
      result.error === 'popup_blocked'
        ? (result.errorDescription ?? 'Popups are blocked.')
        : (result.errorDescription ??
          `The upstream OAuth flow failed (${result.error ?? 'unknown error'}). The server has not been saved.`)
    console.log('[McpServerNew] Setting error state message:', message)
    errorState.value = {
      title: 'Authorization failed',
      message,
      retry: () => {
        errorState.value = null
        void submit()
      },
    }
    return false
  }
  return true
}

async function submit() {
  if (!canSubmit.value) return
  submitting.value = true
  errorState.value = null
  let createdPublicId = ''
  try {
    const resp = await adminClient().createUpstream(
      create(CreateUpstreamRequestSchema, {
        identifier: form.identifier.trim(),
        displayName: form.displayName.trim(),
        mcpUrl: form.mcpUrl.trim(),
        strategyType: form.strategyType,
        strategySubMode: form.strategySubMode,
        strategyConfig: buildStrategyConfig(),
        staticHeaderMode:
          form.strategyType === 'static_header'
            ? form.strategySubMode === 'user'
              ? StaticHeaderMode.OVERRIDE
              : StaticHeaderMode.SHARED
            : StaticHeaderMode.UNSPECIFIED,
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
    createdPublicId = resp.upstream?.publicId ?? ''

    if (resp.requiresAdminLink) {
      const ok = await runOAuthPopup(form.identifier.trim(), createdPublicId)
      if (!ok) return
    }
    successUpstream.value = {
      name: form.displayName.trim() || form.identifier.trim(),
      publicId: createdPublicId,
    }
    successOpen.value = true
  } catch (err) {
    errorState.value = provisionErrorState(err)
  } finally {
    submitting.value = false
  }
}

function goToList() {
  successOpen.value = false
  void router.push(ROUTES.mcpServers)
}

function goToDetail() {
  const id = successUpstream.value?.publicId
  successOpen.value = false
  if (id) {
    void router.push(ROUTES.mcpServerDetail.replace(':id', id))
  } else {
    void router.push(ROUTES.mcpServers)
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
      <p class="mt-1 text-sm text-on-surface-variant">
        Connect a new Model Context Protocol (MCP) server. We'll probe it with the chosen
        authentication strategy before the configuration is saved.
      </p>
    </div>

    <form class="space-y-stack-lg" data-testid="upstream-new-form" @submit.prevent="submit">
      <section class="rounded-xl border border-outline-variant bg-surface-container-lowest">
        <header class="rounded-t-xl border-b border-outline-variant bg-surface-container px-4 py-3">
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
                v-if="!form.identifierAutoDerived"
                type="button"
                class="text-xs font-normal text-primary hover:underline"
                @click="resetNameDerivation"
              >
                Re-derive from display name
              </button>
            </span>
            <input
              :value="form.identifier"
              type="text"
              required
              pattern="[a-z0-9_]+"
              placeholder="auto"
              class="mt-1 block w-full rounded-md border bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:outline-none focus:ring-1"
              :class="
                nameError && form.identifier !== ''
                  ? 'border-error focus:border-error focus:ring-error'
                  : 'border-outline-variant focus:border-primary focus:ring-primary'
              "
              data-testid="field-name"
              @input="onNameInput"
            />
            <span
              v-if="nameError && form.identifier !== ''"
              class="mt-1 block text-xs text-error"
              data-testid="field-name-error"
            >
              {{ nameError }}
            </span>
            <span v-else class="mt-1 block text-xs text-on-surface-variant">
              Stable identifier used in tool prefixes. Auto-derived from the display name; edit only
              if you need a specific slug.
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
                  ? 'bg-primary-container text-on-primary-container'
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
        <header class="rounded-t-xl border-b border-outline-variant bg-surface-container px-4 py-3">
          <h2 class="text-base font-semibold text-on-surface">Header configuration</h2>
        </header>
        <div class="space-y-stack-md p-4">
          <fieldset class="space-y-2">
            <legend class="text-sm font-medium text-on-surface">Secret resolution mode</legend>
            <div
              class="inline-flex rounded-md border border-outline-variant bg-surface-container p-1"
            >
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
                Tenant provided
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
                BYOK
              </button>
            </div>
            <p class="text-xs text-on-surface-variant">
              <strong>Tenant provided</strong> — One global secret for all members. Users cannot
              override.<br />
              <strong>BYOK</strong> — Each member must supply their own API key. The key you enter
              below is your personal key to test the connection. Your team must enter their own keys
              from the portal.
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
          <label class="block border-t border-dashed border-outline-variant pt-stack-md">
            <span class="flex items-center justify-between text-sm font-medium text-on-surface">
              <template v-if="form.strategySubMode === 'tenant'"> Shared tenant secret </template>
              <template v-else> Your API key </template>
              <span class="text-xs font-normal text-on-surface-variant">Required</span>
            </span>
            <input
              v-model="form.apiKey"
              type="password"
              placeholder="Enter secure token or API key"
              class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
              data-testid="field-api-key"
            />
            <span class="mt-1 block text-xs text-on-surface-variant">
              <template v-if="form.strategySubMode === 'tenant'">
                Used to authenticate all requests from Limen to this MCP server.
              </template>
              <template v-else>
                Used to test the connection and set up your first link. Individual users can replace
                this with their own API key after the server is added.
              </template>
            </span>
          </label>
        </div>
      </section>

      <section
        v-if="form.strategyType === 'mcp_spec'"
        class="rounded-xl border border-outline-variant bg-surface-container-lowest"
      >
        <button
          type="button"
          class="flex w-full items-center justify-between gap-3 px-4 py-3 text-left"
          :aria-expanded="staticClientOpen"
          data-testid="static-client-toggle"
          @click="staticClientOpen = !staticClientOpen"
        >
          <span>
            <span class="block text-base font-semibold text-on-surface"
              >Static OAuth client (optional)</span
            >
            <span class="mt-0.5 block text-xs text-on-surface-variant">
              Only needed when the authorization server does not support Dynamic Client
              Registration.
            </span>
          </span>
          <ChevronDown
            :size="20"
            aria-hidden="true"
            class="shrink-0 text-on-surface-variant transition-transform"
            :class="staticClientOpen ? 'rotate-180' : ''"
          />
        </button>
        <div
          v-if="staticClientOpen"
          class="space-y-stack-md border-t border-outline-variant p-4"
          data-testid="static-client-panel"
        >
          <p class="text-xs text-on-surface-variant">
            Provide a pre-registered OAuth client to use instead of DCR. Leave blank to let Limen
            register itself dynamically.
          </p>
          <div class="grid gap-stack-md md:grid-cols-2">
            <label class="block">
              <span class="text-sm font-medium text-on-surface">Client ID</span>
              <input
                v-model="form.oauthClientId"
                type="text"
                class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
                data-testid="field-oauth-client-id"
              />
            </label>
            <label class="block">
              <span class="text-sm font-medium text-on-surface">Client secret</span>
              <input
                v-model="form.oauthClientSecret"
                type="password"
                class="mt-1 block w-full rounded-md border border-outline-variant bg-surface px-3 py-2 font-mono text-sm text-on-surface focus:border-primary focus:outline-none focus:ring-1 focus:ring-primary"
                data-testid="field-oauth-client-secret"
              />
            </label>
          </div>
        </div>
      </section>

      <section class="rounded-xl border border-outline-variant bg-surface-container-lowest">
        <button
          type="button"
          class="flex w-full items-center justify-between gap-3 px-4 py-3 text-left"
          :aria-expanded="defaultsOpen"
          data-testid="defaults-toggle"
          @click="defaultsOpen = !defaultsOpen"
        >
          <span>
            <span class="block text-base font-semibold text-on-surface"
              >Ambient context (optional)</span
            >
            <span class="mt-0.5 block text-xs text-on-surface-variant">
              Pre-filled values the LLM can use without asking the user — Atlassian
              <code class="font-mono">cloudId</code>, Sentry
              <code class="font-mono">organization_slug</code>, Cloudflare
              <code class="font-mono">account_id</code>, default project keys, region names, and
              other stable identifiers this MCP server expects on most tool calls.
            </span>
          </span>
          <ChevronDown
            :size="20"
            aria-hidden="true"
            class="shrink-0 text-on-surface-variant transition-transform"
            :class="defaultsOpen ? 'rotate-180' : ''"
          />
        </button>
        <div
          v-if="defaultsOpen"
          class="space-y-stack-md border-t border-outline-variant p-4"
          data-testid="defaults-panel"
        >
          <p class="text-xs text-on-surface-variant">
            Provide a JSON object whose keys are merged into every tool call's arguments as
            defaults. Tool calls may still override any field. Leave empty if the server needs no
            ambient context.
          </p>
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
          class="inline-flex items-center gap-1.5 rounded-md bg-primary px-4 py-2 text-sm font-medium text-on-primary shadow-sm hover:bg-primary/90 disabled:opacity-50"
          data-testid="submit-upstream"
        >
          <Save :size="16" aria-hidden="true" />
          {{ submitting ? 'Testing & saving…' : 'Test & save server' }}
        </button>
      </div>
    </form>

    <ErrorModal
      :open="errorState !== null"
      :title="errorState?.title ?? ''"
      :message="errorState?.message ?? ''"
      :primary-label="errorState?.retry ? 'Try again' : undefined"
      secondary-label="Close"
      @primary="errorState?.retry?.()"
      @secondary="errorState = null"
      @close="errorState = null"
    />

    <SuccessModal
      :open="successOpen"
      title="Connection Successful"
      :chip="successUpstream?.name"
      message="The MCP server has been authenticated and registered. Its tool catalog will be indexed in the background."
      primary-label="Go to MCP Management"
      secondary-label="View Server Details"
      @primary="goToList"
      @secondary="goToDetail"
      @close="goToList"
    />
  </div>
</template>
