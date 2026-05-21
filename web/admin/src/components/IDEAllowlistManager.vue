<script setup lang="ts">
// IDEAllowlistManager — phase 9f.
//
// Two regions:
//
//  1. Common IDEs — a grid of preset cards. Each card derives status
//     from (preset.patterns ∩ tenant entries.{ide_key=preset.key}):
//       active   = all preset patterns present
//       partial  = some present
//       inactive = none present
//     Apply / Remove / Re-apply buttons round-trip
//     AdminService.ApplyIDEPreset / RemoveIDEPreset.
//
//  2. Custom URIs — table of allowlist entries with ide_key == "".
//     Add / Edit / Delete go through Add/Update/RemoveAllowlistEntry.
//
// The component owns its own load + mutation state; the parent only
// embeds it.

import { computed, onMounted, ref } from 'vue'
import { ConnectError } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { ChevronDown, Loader2, Plus, Pencil, Trash2 } from '@lucide/vue'
import { ErrorModal, SuccessModal, validateRedirectURIPattern } from '@limen/shared'
import { adminClient } from '@/transport/adminClient'
import {
    AddAllowlistEntryRequestSchema,
    UpdateAllowlistEntryRequestSchema,
    RemoveAllowlistEntryRequestSchema,
    ApplyIDEPresetRequestSchema,
    RemoveIDEPresetRequestSchema,
    type AllowlistEntry,
    type IDEPreset,
} from '@/gen/limen/admin/v1/admin_pb.ts'

const loading = ref(true)
const presets = ref<IDEPreset[]>([])
const entries = ref<AllowlistEntry[]>([])
const errorState = ref<{ title: string; message: string } | null>(null)
const successOpen = ref(false)
const successMessage = ref('')

const customEntries = computed(() => entries.value.filter((e) => e.ideKey === ''))

interface PresetView {
    preset: IDEPreset
    status: 'active' | 'partial' | 'inactive'
    matched: number
    busy: boolean
}

const presetBusy = ref<Record<string, boolean>>({})

const presetViews = computed<PresetView[]>(() =>
    presets.value.map((p) => {
        const have = new Set(
            entries.value
                .filter((e) => e.ideKey === p.key)
                .map((e) => e.pattern),
        )
        const matched = p.patterns.filter((pat) => have.has(pat)).length
        let status: PresetView['status'] = 'inactive'
        if (matched === p.patterns.length && p.patterns.length > 0) status = 'active'
        else if (matched > 0) status = 'partial'
        return { preset: p, status, matched, busy: presetBusy.value[p.key] ?? false }
    }),
)

async function reload() {
    const [pr, en] = await Promise.all([
        adminClient().listIDEPresets({}),
        adminClient().listAllowlistEntries({}),
    ])
    presets.value = pr.presets
    entries.value = en.entries
}

onMounted(async () => {
    try {
        await reload()
    } catch (err) {
        showError('Failed to load IDE allowlist', err)
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

async function applyPreset(view: PresetView) {
    presetBusy.value = { ...presetBusy.value, [view.preset.key]: true }
    try {
        await adminClient().applyIDEPreset(
            create(ApplyIDEPresetRequestSchema, { ideKey: view.preset.key }),
        )
        await reload()
    } catch (err) {
        showError(`Failed to apply ${view.preset.displayName}`, err)
    } finally {
        presetBusy.value = { ...presetBusy.value, [view.preset.key]: false }
    }
}

async function removePreset(view: PresetView) {
    presetBusy.value = { ...presetBusy.value, [view.preset.key]: true }
    try {
        await adminClient().removeIDEPreset(
            create(RemoveIDEPresetRequestSchema, { ideKey: view.preset.key }),
        )
        await reload()
    } catch (err) {
        showError(`Failed to remove ${view.preset.displayName}`, err)
    } finally {
        presetBusy.value = { ...presetBusy.value, [view.preset.key]: false }
    }
}

// Custom entry editor state.
interface EditorState {
    open: boolean
    publicId: string // empty = new
    label: string
    pattern: string
    patternError: string
    busy: boolean
}
const editor = ref<EditorState>({
    open: false,
    publicId: '',
    label: '',
    pattern: '',
    patternError: '',
    busy: false,
})

function openNew() {
    editor.value = { open: true, publicId: '', label: '', pattern: '', patternError: '', busy: false }
}

function openEdit(e: AllowlistEntry) {
    editor.value = {
        open: true,
        publicId: e.publicId,
        label: e.label,
        pattern: e.pattern,
        patternError: '',
        busy: false,
    }
}

function validatePattern() {
    const res = validateRedirectURIPattern(editor.value.pattern.trim())
    editor.value.patternError = res.ok ? '' : res.reason
    return res.ok
}

async function saveEntry() {
    if (!validatePattern()) return
    editor.value.busy = true
    try {
        if (editor.value.publicId === '') {
            await adminClient().addAllowlistEntry(
                create(AddAllowlistEntryRequestSchema, {
                    ideKey: '',
                    label: editor.value.label.trim(),
                    pattern: editor.value.pattern.trim(),
                }),
            )
        } else {
            await adminClient().updateAllowlistEntry(
                create(UpdateAllowlistEntryRequestSchema, {
                    publicId: editor.value.publicId,
                    label: editor.value.label.trim(),
                    pattern: editor.value.pattern.trim(),
                }),
            )
        }
        editor.value.open = false
        await reload()
        successMessage.value = 'Custom redirect URI saved.'
        successOpen.value = true
    } catch (err) {
        showError('Failed to save custom URI', err)
    } finally {
        editor.value.busy = false
    }
}

async function deleteEntry(e: AllowlistEntry) {
    if (!window.confirm(`Remove ${e.label || e.pattern}?`)) return
    try {
        await adminClient().removeAllowlistEntry(
            create(RemoveAllowlistEntryRequestSchema, { publicId: e.publicId }),
        )
        await reload()
    } catch (err) {
        showError('Failed to remove entry', err)
    }
}
</script>

<template>
    <div class="space-y-stack-md" data-testid="ide-allowlist-manager">
        <div v-if="loading" class="flex items-center gap-2 text-on-surface-variant">
            <Loader2 class="h-4 w-4 animate-spin" />
            Loading IDE presets…
        </div>

        <template v-else>
            <!-- Common IDEs -->
            <section aria-labelledby="ide-common-heading" class="space-y-3">
                <h3 id="ide-common-heading" class="text-base font-semibold text-on-surface">
                    Common IDEs
                </h3>
                <p class="text-sm text-on-surface-variant">
                    One-click presets for the official redirect URIs of popular AI IDEs.
                </p>
                <div class="grid gap-gutter md:grid-cols-2 lg:grid-cols-3">
                    <article v-for="v in presetViews" :key="v.preset.key" class="rounded-lg border p-4"
                        :class="v.status === 'active'
                            ? 'border-primary bg-primary/5'
                            : v.status === 'partial'
                                ? 'border-warning/40 bg-warning/5'
                                : 'border-outline-variant bg-surface'" :data-testid="`ide-preset-${v.preset.key}`">
                        <header class="flex items-center justify-between">
                            <h4 class="font-semibold text-on-surface">{{ v.preset.displayName }}</h4>
                            <span class="text-xs uppercase tracking-wide" :class="v.status === 'active'
                                ? 'text-primary'
                                : v.status === 'partial'
                                    ? 'text-warning'
                                    : 'text-on-surface-variant'
                                ">{{ v.status }}</span>
                        </header>
                        <p class="mt-1 text-xs text-on-surface-variant">
                            {{ v.matched }} / {{ v.preset.patterns.length }} patterns active
                        </p>
                        <details class="mt-2 group" :data-testid="`patterns-${v.preset.key}`">
                            <summary class="flex cursor-pointer items-center gap-1 text-xs text-on-surface-variant hover:text-on-surface select-none">
                                <ChevronDown class="h-3 w-3 transition-transform group-open:rotate-180" />
                                Show redirect URIs
                            </summary>
                            <ul class="mt-2 space-y-1 pl-4">
                                <li v-for="pat in v.preset.patterns" :key="pat"
                                    class="font-mono text-[11px] leading-tight text-on-surface-variant break-all">
                                    {{ pat }}
                                </li>
                            </ul>
                        </details>
                        <div class="mt-3 flex flex-wrap gap-2">
                            <button v-if="v.status !== 'active'" type="button"
                                class="rounded bg-primary px-3 py-1.5 text-xs font-medium text-on-primary hover:bg-primary/90 disabled:opacity-50"
                                :disabled="v.busy" :data-testid="`apply-${v.preset.key}`" @click="applyPreset(v)">
                                {{ v.status === 'partial' ? 'Complete' : 'Apply' }}
                            </button>
                            <button v-if="v.status !== 'inactive'" type="button"
                                class="rounded border border-outline px-3 py-1.5 text-xs text-on-surface hover:bg-surface-variant disabled:opacity-50"
                                :disabled="v.busy" :data-testid="`remove-${v.preset.key}`" @click="removePreset(v)">
                                Remove
                            </button>
                        </div>
                    </article>
                </div>
            </section>

            <!-- Custom URIs -->
            <section aria-labelledby="ide-custom-heading" class="space-y-3">
                <div class="flex items-center justify-between">
                    <h3 id="ide-custom-heading" class="text-base font-semibold text-on-surface">
                        Custom redirect URIs
                    </h3>
                    <button type="button"
                        class="inline-flex items-center gap-1 rounded bg-primary px-3 py-1.5 text-sm font-medium text-on-primary hover:bg-primary/90"
                        data-testid="custom-add" @click="openNew">
                        <Plus class="h-4 w-4" />
                        Add URI
                    </button>
                </div>
                <p class="text-sm text-on-surface-variant">
                    Patterns added here apply on top of the IDE presets above. The
                    global HTTPS / loopback floor always applies.
                </p>
                <div v-if="customEntries.length === 0" class="rounded border border-outline-variant p-4 text-sm text-on-surface-variant"
                    data-testid="custom-empty">
                    No custom URIs yet.
                </div>
                <table v-else class="min-w-full text-sm" data-testid="custom-table">
                    <thead class="text-left text-xs uppercase tracking-wide text-on-surface-variant">
                        <tr>
                            <th class="px-2 py-1">Label</th>
                            <th class="px-2 py-1">Pattern</th>
                            <th class="px-2 py-1"></th>
                        </tr>
                    </thead>
                    <tbody>
                        <tr v-for="e in customEntries" :key="e.publicId" class="border-t border-outline-variant">
                            <td class="px-2 py-2 text-on-surface">{{ e.label || '—' }}</td>
                            <td class="px-2 py-2 font-mono text-xs text-on-surface">{{ e.pattern }}</td>
                            <td class="px-2 py-2 text-right">
                                <button type="button" class="p-1 text-on-surface-variant hover:text-on-surface"
                                    :data-testid="`edit-${e.publicId}`" @click="openEdit(e)">
                                    <Pencil class="h-4 w-4" />
                                </button>
                                <button type="button" class="ml-1 p-1 text-on-surface-variant hover:text-error"
                                    :data-testid="`delete-${e.publicId}`" @click="deleteEntry(e)">
                                    <Trash2 class="h-4 w-4" />
                                </button>
                            </td>
                        </tr>
                    </tbody>
                </table>
            </section>

            <!-- Editor modal -->
            <div v-if="editor.open"
                class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
                data-testid="custom-editor" @click.self="editor.open = false">
                <div class="w-full max-w-md space-y-3 rounded-lg bg-surface p-6 text-on-surface shadow-lg">
                    <h3 class="text-lg font-semibold">
                        {{ editor.publicId === '' ? 'Add custom redirect URI' : 'Edit custom redirect URI' }}
                    </h3>
                    <label class="block text-sm font-medium" for="custom-label">Label</label>
                    <input id="custom-label" v-model="editor.label" type="text"
                        class="w-full rounded border border-outline bg-surface px-3 py-2 text-sm focus:border-primary focus:outline-none"
                        data-testid="custom-label-input" />
                    <label class="block text-sm font-medium" for="custom-pattern">Pattern</label>
                    <input id="custom-pattern" v-model="editor.pattern" type="text"
                        class="w-full rounded border border-outline bg-surface px-3 py-2 font-mono text-sm focus:border-primary focus:outline-none"
                        data-testid="custom-pattern-input" @blur="validatePattern" />
                    <p v-if="editor.patternError" class="text-xs text-error" data-testid="custom-pattern-error">
                        {{ editor.patternError }}
                    </p>
                    <div class="flex justify-end gap-2 pt-2">
                        <button type="button"
                            class="rounded border border-outline px-3 py-1.5 text-sm hover:bg-surface-variant"
                            data-testid="custom-cancel" @click="editor.open = false">
                            Cancel
                        </button>
                        <button type="button"
                            class="rounded bg-primary px-3 py-1.5 text-sm font-medium text-on-primary hover:bg-primary/90 disabled:opacity-50"
                            :disabled="editor.busy || editor.pattern.trim() === ''"
                            data-testid="custom-save" @click="saveEntry">
                            Save
                        </button>
                    </div>
                </div>
            </div>
        </template>

        <SuccessModal :open="successOpen" title="Saved" :message="successMessage" @close="successOpen = false" />
        <ErrorModal :open="errorState !== null" :title="errorState?.title ?? ''"
            :message="errorState?.message ?? ''" @close="errorState = null" />
    </div>
</template>
