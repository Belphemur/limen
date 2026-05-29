// Shared composable that encapsulates upstream linking actions (OAuth
// connect, API key submission, enable/disable toggle, disconnect) across
// the Admin and Portal SPAs.
//
// The caller provides an adapter that maps generic actions to the
// specific RPCs for their context (SA-prefixed admin RPCs or bare portal
// RPCs). The composable handles:
//   - Busy state per row (prevents double-clicks)
//   - API key modal lifecycle
//   - OAuth popup orchestration (popup or redirect mode)
//   - Destructive-action confirmation (optional)
//   - Error forwarding via onError callback
//   - Refresh after every successful mutation
//
// Usage:
//
//   const actions = useUpstreamActions({
//     adapter: {
//       startConnect: (up, ret) => rpc(...),
//       submitKey: (up, key) => rpc(...),
//       setEnabled: (up, en) => rpc(...),
//       disconnect: (up) => rpc(...),
//     },
//     oauthMode: 'popup',
//     onRefresh: () => refreshList(),
//     onError: (msg) => (error.value = msg),
//   })

import { ref, computed, type Ref } from 'vue'
import { openOAuthPopup } from './upstreamOAuthPopup'
import { type CTAKind } from './upstream-cta'

// ── Types ─────────────────────────────────────────────────────────────

/** Minimal upstream shape consumed by the adapter. Both the admin and
 *  portal SPAs use protobuf-generated `UpstreamSummary` which satisfies
 *  this structurally. */
export interface UpstreamLike {
  publicId: string
  identifier: string
  displayName?: string
}

/** Adapter bridging generic actions to context-specific RPCs.
 *  Each method receives the full upstream object so the caller can
 *  extract `publicId`, `identifier`, or any other field as needed. */
export interface UpstreamActionsAdapter {
  startConnect: (upstream: UpstreamLike, returnTo: string) => Promise<string>
  submitKey: (upstream: UpstreamLike, apiKey: string) => Promise<void>
  setEnabled: (upstream: UpstreamLike, enabled: boolean) => Promise<void>
  disconnect: (upstream: UpstreamLike) => Promise<void>
}

export interface UpstreamActionsOptions {
  /** Adapter that translates actions to RPC calls. */
  adapter: UpstreamActionsAdapter
  /** Called after every successful mutation to keep the UI fresh. */
  onRefresh: () => Promise<void>
  /** How OAuth connect is initiated.
   *  - 'popup': opens a popup window via openOAuthPopup (admin SPA).
   *  - 'redirect': navigates the current window (portal SPA). */
  oauthMode: 'popup' | 'redirect'
  /** Optional callback fired when any action produces an error. */
  onError?: (message: string) => void
  /** When true, `disconnect` shows a native confirm dialog before executing.
   *  Default false. */
  confirmDestructive?: boolean
}

export interface ApiKeyTarget {
  identifier: string
  label: string
  title?: string
}

export interface UseUpstreamActionsReturn {
  /** Per-row busy map: upstreamPublicId → action kind. Reset to ''
   *  after completion. */
  busyByRow: Ref<Record<string, string>>
  /** True when ANY row is busy. */
  readonly anyBusy: Ref<boolean>
  /** Returns the action that a given upstream is busy with, or ''. */
  busyAction: (upstreamPublicId: string) => string
  /** True when the specific upstream is busy with ANY action. */
  isBusy: (upstreamPublicId: string) => boolean
  /** True when the specific upstream is busy with the given action. */
  isBusyWith: (upstreamPublicId: string, action: CTAKind) => boolean

  /** API key modal state. */
  apiKeyModalOpen: Ref<boolean>
  apiKeyTarget: Ref<ApiKeyTarget | null>
  apiKeyBusy: Ref<boolean>

  /** Dispatch an upstream action by CTA kind. */
  handleAction: (upstream: UpstreamLike, action: CTAKind) => Promise<void>
  /** Submit the API key from the modal. */
  submitApiKey: (apiKey: string) => Promise<void>
  /** Close the API key modal without submitting. */
  cancelApiKeyModal: () => void
  /** Clear the busy state for a given upstream. */
  clearBusy: (publicId: string) => void
}

// ── Composable ────────────────────────────────────────────────────────

export function useUpstreamActions(options: UpstreamActionsOptions): UseUpstreamActionsReturn {
  // ── Busy state ────────────────────────────────────────────────────
  const busyByRow = ref<Record<string, string>>({})

  const anyBusy = computed(() => Object.values(busyByRow.value).some((a) => a !== ''))
  function setBusy(publicId: string, action: string): void {
    busyByRow.value = { ...busyByRow.value, [publicId]: action }
  }

  function clearBusy(publicId: string): void {
    setBusy(publicId, '')
  }

  function busyAction(upstreamPublicId: string): string {
    return busyByRow.value[upstreamPublicId] ?? ''
  }

  function isBusy(upstreamPublicId: string): boolean {
    return !!busyByRow.value[upstreamPublicId]
  }

  function isBusyWith(upstreamPublicId: string, action: CTAKind): boolean {
    return busyByRow.value[upstreamPublicId] === action
  }

  // ── API key modal ─────────────────────────────────────────────────
  const apiKeyModalOpen = ref(false)
  const apiKeyTarget = ref<ApiKeyTarget | null>(null)
  const apiKeyBusy = ref(false)

  // ── Error helper ──────────────────────────────────────────────────
  function emitError(err: unknown): void {
    const msg = err instanceof Error ? err.message : String(err)
    options.onError?.(msg)
  }

  // ── Action dispatch ───────────────────────────────────────────────
  async function handleAction(upstream: UpstreamLike, action: CTAKind): Promise<void> {
    // Guard: prevent double-clicks
    if (busyByRow.value[upstream.publicId]) return

    // Modal-opening actions don't set busy — the modal has its own busy state
    if (action === 'submitKey' || action === 'rotateKey') {
      apiKeyTarget.value = {
        identifier: upstream.identifier,
        label: upstream.displayName || upstream.identifier,
        title:
          action === 'rotateKey'
            ? `Rotate API key for ${upstream.displayName || upstream.identifier}`
            : undefined,
      }
      apiKeyModalOpen.value = true
      return
    }

    // Destructive-action confirmation
    if (options.confirmDestructive) {
      if (action === 'disconnect') {
        if (
          !window.confirm(
            `Disconnect ${upstream.displayName || upstream.identifier}? This removes stored credentials.`,
          )
        ) {
          return
        }
      }
    }

    setBusy(upstream.publicId, action)
    try {
      switch (action) {
        case 'connect': {
          const url = await options.adapter.startConnect(upstream, window.location.pathname)
          if (options.oauthMode === 'popup') {
            const result = await openOAuthPopup({ url })
            if (!result.ok) {
              emitError(result.errorDescription || result.error || 'OAuth failed')
              return
            }
          } else {
            window.location.assign(url)
            return
          }
          break
        }
        case 'enable':
          await options.adapter.setEnabled(upstream, true)
          break
        case 'disable':
          await options.adapter.setEnabled(upstream, false)
          break
        case 'disconnect':
          await options.adapter.disconnect(upstream)
          break
        default:
          throw new Error(`Unknown upstream action: ${action}`)
      }
      await options.onRefresh()
    } catch (err) {
      emitError(err)
    } finally {
      clearBusy(upstream.publicId)
    }
  }

  async function submitApiKey(apiKey: string): Promise<void> {
    const target = apiKeyTarget.value
    if (!target) return
    apiKeyBusy.value = true
    try {
      await options.adapter.submitKey(
        { publicId: '', identifier: target.identifier, displayName: target.label },
        apiKey,
      )
      apiKeyModalOpen.value = false
      await options.onRefresh()
    } catch (err) {
      emitError(err)
    } finally {
      apiKeyBusy.value = false
    }
  }

  function cancelApiKeyModal(): void {
    apiKeyModalOpen.value = false
    apiKeyTarget.value = null
  }

  return {
    busyByRow,
    anyBusy,
    busyAction,
    isBusy,
    isBusyWith,
    apiKeyModalOpen,
    apiKeyTarget,
    apiKeyBusy,
    handleAction,
    submitApiKey,
    cancelApiKeyModal,
    clearBusy,
  }
}
