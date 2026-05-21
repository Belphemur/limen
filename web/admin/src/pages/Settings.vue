<script setup lang="ts">
// Organization Settings — phase 9c slice 3.
//
// Four sections, each backed by a single round-trip:
//
//  1. Organization     → UpdateTenantSettings({ name })
//  2. Zitadel identity → readonly; deep-links into Console via the
//                        shared zitadelConsoleUrl helper
//  3. DCR allowlist    → UpdateTenantSettings({ dcrRedirectUriAllowlist,
//                                                 dcrRedirectUriAllowlistSet: true })
//  4. Danger Zone      → DeleteTenant({ publicIdConfirmation })
//
// Initial state is hydrated from a single GetTenantSettings call plus
// the cached /auth/discovery fetch.

import { computed, onMounted, ref } from 'vue'
import { ConnectError } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { AlertTriangle, Copy, ExternalLink, Loader2, Save, Trash2 } from '@lucide/vue'
import {
    ErrorModal,
    SuccessModal,
    RedirectURIAllowlistEditor,
    fetchDiscovery,
    zitadelConsoleUrl,
} from '@limen/shared'
import { adminClient } from '@/transport/adminClient'
import {
    DeleteTenantRequestSchema,
    UpdateTenantSettingsRequestSchema,
} from '@/gen/limen/admin/v1/admin_pb.ts'

interface Loaded {
    name: string
    publicId: string
    zitadelOrgId: string
    allowlist: string[]
}

const loading = ref(true)
const loadError = ref<string | null>(null)
const loaded = ref<Loaded | null>(null)
const issuer = ref('')

// Editable buffers; reset to the freshly-loaded values whenever a save
// succeeds (or the page loads).
const nameDraft = ref('')
const allowlistDraft = ref<string[]>([])
const allowlistValid = ref(true)

const savingName = ref(false)
const savingAllowlist = ref(false)
const deleteOpen = ref(false)
const deleting = ref(false)
const deleteConfirmation = ref('')

const successOpen = ref(false)
const successMessage = ref('')
interface ErrorState {
    title: string
    message: string
}
const errorState = ref<ErrorState | null>(null)

const consoleUrl = computed(() =>
    loaded.value
        ? zitadelConsoleUrl(issuer.value, loaded.value.zitadelOrgId, 'users')
        : '',
)

const nameDirty = computed(
    () =>
        loaded.value !== null &&
        nameDraft.value.trim() !== loaded.value.name &&
        nameDraft.value.trim() !== '',
)
const allowlistDirty = computed(() => {
    if (!loaded.value) return false
    const a = loaded.value.allowlist
    const b = allowlistDraft.value
    if (a.length !== b.length) return true
    return a.some((v, i) => v !== b[i])
})

const deleteConfirmed = computed(
    () =>
        loaded.value !== null && deleteConfirmation.value === loaded.value.publicId,
)

function hydrate(name: string, publicId: string, zitadelOrgId: string, allowlist: string[]) {
    loaded.value = { name, publicId, zitadelOrgId, allowlist: [...allowlist] }
    nameDraft.value = name
    allowlistDraft.value = [...allowlist]
}

onMounted(async () => {
    try {
        const [settings, discovery] = await Promise.all([
            adminClient().getTenantSettings({}),
            fetchDiscovery().catch(() => ({ zitadelIssuer: '' })),
        ])
        issuer.value = discovery.zitadelIssuer
        hydrate(
            settings.settings?.name ?? '',
            settings.settings?.publicId ?? '',
            settings.zitadelOrgId,
            settings.dcrRedirectUriAllowlist,
        )
    } catch (err) {
        loadError.value = err instanceof Error ? err.message : String(err)
    } finally {
        loading.value = false
    }
})

function showError(title: string, err: unknown) {
    const message =
        err instanceof ConnectError
            ? err.message
            : err instanceof Error
                ? err.message
                : String(err)
    errorState.value = { title, message }
}

function copyPublicId() {
    if (!loaded.value) return
    void navigator.clipboard?.writeText(loaded.value.publicId)
}

async function saveName() {
    if (!loaded.value || !nameDirty.value) return
    savingName.value = true
    try {
        const resp = await adminClient().updateTenantSettings(
            create(UpdateTenantSettingsRequestSchema, { name: nameDraft.value.trim() }),
        )
        hydrate(
            resp.settings?.name ?? nameDraft.value.trim(),
            loaded.value.publicId,
            loaded.value.zitadelOrgId,
            resp.dcrRedirectUriAllowlist,
        )
        successMessage.value = 'Organization name updated.'
        successOpen.value = true
    } catch (err) {
        showError('Failed to update organization name', err)
    } finally {
        savingName.value = false
    }
}

async function saveAllowlist() {
    if (!loaded.value || !allowlistDirty.value || !allowlistValid.value) return
    savingAllowlist.value = true
    try {
        const resp = await adminClient().updateTenantSettings(
            create(UpdateTenantSettingsRequestSchema, {
                dcrRedirectUriAllowlist: allowlistDraft.value,
                dcrRedirectUriAllowlistSet: true,
            }),
        )
        hydrate(
            resp.settings?.name ?? loaded.value.name,
            loaded.value.publicId,
            loaded.value.zitadelOrgId,
            resp.dcrRedirectUriAllowlist,
        )
        successMessage.value = 'DCR redirect-URI allowlist updated.'
        successOpen.value = true
    } catch (err) {
        showError('Failed to update allowlist', err)
    } finally {
        savingAllowlist.value = false
    }
}

const dialogRef = ref<HTMLDialogElement | null>(null)

function openDelete() {
    deleteConfirmation.value = ''
    deleteOpen.value = true
    queueMicrotask(() => dialogRef.value?.showModal())
}

function closeDelete() {
    dialogRef.value?.close()
    deleteOpen.value = false
}

async function confirmDelete() {
    if (!loaded.value || !deleteConfirmed.value) return
    deleting.value = true
    try {
        await adminClient().deleteTenant(
            create(DeleteTenantRequestSchema, {
                publicIdConfirmation: loaded.value.publicId,
            }),
        )
        window.location.assign('/forbidden')
    } catch (err) {
        showError('Failed to delete organization', err)
    } finally {
        deleting.value = false
    }
}
</script>

<template>
    <div class="space-y-stack-lg">
        <header>
            <h1 class="font-display text-3xl font-bold tracking-tight text-on-surface">
                Organization Settings
            </h1>
            <p class="mt-2 max-w-2xl text-sm text-on-surface-variant">
                Configure your organization name, the DCR redirect-URI allowlist, and
                cross-reference your Zitadel identity. Destructive actions live in the
                Danger Zone at the bottom.
            </p>
        </header>

        <div v-if="loading" class="flex items-center gap-2 text-on-surface-variant" data-testid="settings-loading">
            <Loader2 class="h-4 w-4 animate-spin" />
            Loading settings…
        </div>

        <p v-else-if="loadError" class="rounded border border-error/40 bg-error/10 p-3 text-sm text-error">
            Failed to load settings: {{ loadError }}
        </p>

        <template v-else-if="loaded">
            <!-- 1. Organization -->
            <section aria-labelledby="org-heading"
                class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
                data-testid="section-organization">
                <h2 id="org-heading" class="text-lg font-semibold text-on-surface">
                    Organization
                </h2>
                <label for="org-name" class="block text-sm font-medium text-on-surface">
                    Name
                </label>
                <input id="org-name" v-model="nameDraft" type="text"
                    class="w-full max-w-md rounded border border-outline bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none"
                    data-testid="org-name-input" />

                <div>
                    <label class="block text-sm font-medium text-on-surface">Public ID</label>
                    <div class="mt-1 flex items-center gap-2">
                        <code class="rounded bg-surface-variant px-2 py-1 font-mono text-sm text-on-surface"
                            data-testid="org-public-id">{{ loaded.publicId }}</code>
                        <button type="button"
                            class="rounded p-1.5 text-on-surface-variant hover:bg-surface-variant focus:outline-none focus:ring-2 focus:ring-primary"
                            aria-label="Copy public ID" data-testid="org-public-id-copy" @click="copyPublicId">
                            <Copy class="h-4 w-4" />
                        </button>
                    </div>
                </div>

                <div class="pt-2">
                    <button type="button"
                        class="inline-flex items-center gap-1 rounded bg-primary px-4 py-2 text-sm font-medium text-on-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
                        :disabled="!nameDirty || savingName" data-testid="org-save" @click="saveName">
                        <Save v-if="!savingName" class="h-4 w-4" />
                        <Loader2 v-else class="h-4 w-4 animate-spin" />
                        Save name
                    </button>
                </div>
            </section>

            <!-- 2. Zitadel identity -->
            <section aria-labelledby="zitadel-heading"
                class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6" data-testid="section-zitadel">
                <h2 id="zitadel-heading" class="text-lg font-semibold text-on-surface">
                    Zitadel identity
                </h2>
                <p class="text-sm text-on-surface-variant">
                    Limen mirrors your organization 1:1 against a Zitadel org. Users,
                    roles, and SSO configuration are owned by Zitadel.
                </p>
                <div>
                    <label class="block text-sm font-medium text-on-surface">Zitadel Org ID</label>
                    <code
                        class="mt-1 inline-block rounded bg-surface-variant px-2 py-1 font-mono text-sm text-on-surface"
                        data-testid="zitadel-org-id">{{ loaded.zitadelOrgId }}</code>
                </div>
                <a v-if="consoleUrl" :href="consoleUrl" target="_blank" rel="noopener noreferrer"
                    class="inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline"
                    data-testid="zitadel-console-link">
                    Manage users in Zitadel Console
                    <ExternalLink class="h-4 w-4" />
                </a>
                <p v-else class="text-sm text-on-surface-variant">
                    Zitadel Console link is unavailable — the deployment is missing an
                    issuer in /auth/discovery.
                </p>
            </section>

            <!-- 3. DCR allowlist -->
            <section aria-labelledby="allowlist-heading"
                class="space-y-3 rounded-lg border border-outline-variant bg-surface p-6"
                data-testid="section-allowlist">
                <h2 id="allowlist-heading" class="text-lg font-semibold text-on-surface">
                    DCR redirect-URI allowlist
                </h2>
                <p class="text-sm text-on-surface-variant">
                    Glob patterns narrowing what redirect URIs may be registered via DCR.
                    The global HTTPS / loopback floor always applies; an empty list means
                    "floor only".
                </p>
                <RedirectURIAllowlistEditor v-model="allowlistDraft" :disabled="savingAllowlist"
                    @validity-change="allowlistValid = $event" />
                <div class="pt-2">
                    <button type="button"
                        class="inline-flex items-center gap-1 rounded bg-primary px-4 py-2 text-sm font-medium text-on-primary hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-50"
                        :disabled="!allowlistDirty || !allowlistValid || savingAllowlist" data-testid="allowlist-save"
                        @click="saveAllowlist">
                        <Save v-if="!savingAllowlist" class="h-4 w-4" />
                        <Loader2 v-else class="h-4 w-4 animate-spin" />
                        Save allowlist
                    </button>
                </div>
            </section>

            <!-- 4. Danger zone -->
            <section aria-labelledby="danger-heading" class="space-y-3 rounded-lg border border-error/40 bg-error/5 p-6"
                data-testid="section-danger">
                <h2 id="danger-heading" class="flex items-center gap-2 text-lg font-semibold text-error">
                    <AlertTriangle class="h-5 w-5" />
                    Danger Zone
                </h2>
                <p class="text-sm text-on-surface-variant">
                    Deleting the organization soft-deletes every upstream, user binding,
                    and per-user link Limen knows about. The Zitadel org is left
                    untouched — its lifecycle is owned outside Limen.
                </p>
                <button type="button"
                    class="inline-flex items-center gap-1 rounded border border-error bg-error px-4 py-2 text-sm font-medium text-on-error hover:bg-error/90 focus:outline-none focus:ring-2 focus:ring-error"
                    data-testid="danger-open" @click="openDelete">
                    <Trash2 class="h-4 w-4" />
                    Delete organization…
                </button>
            </section>

            <dialog v-if="deleteOpen" ref="dialogRef"
                class="rounded-lg border border-error/40 bg-surface p-6 text-on-surface backdrop:bg-black/50"
                data-testid="danger-dialog" @close="deleteOpen = false">
                <h3 class="text-lg font-semibold text-error">Delete organization</h3>
                <p class="mt-2 max-w-md text-sm text-on-surface-variant">
                    This action cannot be undone. Type the public ID
                    <code class="rounded bg-surface-variant px-1 py-0.5 font-mono text-xs">{{ loaded.publicId }}</code>
                    to confirm.
                </p>
                <input v-model="deleteConfirmation" type="text"
                    class="mt-3 w-full rounded border border-outline bg-surface px-3 py-2 font-mono text-sm focus:border-error focus:outline-none"
                    :placeholder="loaded.publicId" autocomplete="off" spellcheck="false"
                    data-testid="danger-confirm-input" />
                <div class="mt-4 flex justify-end gap-2">
                    <button type="button"
                        class="rounded border border-outline px-4 py-2 text-sm text-on-surface hover:bg-surface-variant"
                        data-testid="danger-cancel" @click="closeDelete">
                        Cancel
                    </button>
                    <button type="button"
                        class="inline-flex items-center gap-1 rounded bg-error px-4 py-2 text-sm font-medium text-on-error hover:bg-error/90 focus:outline-none focus:ring-2 focus:ring-error disabled:opacity-50"
                        :disabled="!deleteConfirmed || deleting" data-testid="danger-confirm" @click="confirmDelete">
                        <Loader2 v-if="deleting" class="h-4 w-4 animate-spin" />
                        <Trash2 v-else class="h-4 w-4" />
                        Delete forever
                    </button>
                </div>
            </dialog>
        </template>

        <SuccessModal :open="successOpen" title="Saved" :message="successMessage" @close="successOpen = false" />
        <ErrorModal :open="errorState !== null" :title="errorState?.title ?? ''" :message="errorState?.message ?? ''"
            @close="errorState = null" />
    </div>
</template>
