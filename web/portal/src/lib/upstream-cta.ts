import { LinkState, type UpstreamSummary } from '@gen/limen/portal/v1/portal_pb.js'

// CTAKind enumerates the actions the Upstreams page can offer per
// row. `connect` covers both the initial OAuth dance (mcp_spec) and
// the re-link after needs_relink. `submitKey` opens the API key
// modal for static_header user-mode (initial or rotation).
export type CTAKind = 'connect' | 'submitKey' | 'rotateKey' | 'enable' | 'disable' | 'disconnect'

export interface CTA {
  kind: CTAKind
  label: string
  // primary = visually emphasized button; secondary = plain.
  variant: 'primary' | 'secondary' | 'danger'
}

// upstreamCTAs returns the ordered list of CTAs for a given upstream
// row. Returning an empty array means "tools already available, no
// user action needed" — true for `none` strategies and the tenant
// sub-mode of `static_header`.
//
// The decision table mirrors phase-09b-portal-spa.md §Design:
//
//   requires_link=false                       → []  (no CTA)
//   strategy=mcp_spec,        state=NONE      → [Connect]
//   strategy=static_header.u, state=NONE      → [Enter API key]
//   state=CONNECTED                           → [Disable, Disconnect, (Rotate)]
//   state=DISABLED                            → [Enable, Disconnect]
//   state=AUTO_DISABLED                       → [Re-enable, Disconnect]
//   state=NEEDS_RELINK (mcp_spec)             → [Reconnect, Disconnect]
//   state=NEEDS_RELINK (static_header.user)   → [Re-enter key, Disconnect]
export function upstreamCTAs(up: UpstreamSummary): CTA[] {
  if (!up.requiresLink) return []

  const isUserHeader = up.strategyType === 'static_header' && up.strategySubMode === 'user'

  switch (up.linkState) {
    case LinkState.NONE:
      return isUserHeader
        ? [{ kind: 'submitKey', label: 'Enter API key', variant: 'primary' }]
        : [{ kind: 'connect', label: 'Connect', variant: 'primary' }]

    case LinkState.CONNECTED: {
      const ctas: CTA[] = [
        { kind: 'disable', label: 'Disable', variant: 'secondary' },
        { kind: 'disconnect', label: 'Disconnect', variant: 'danger' },
      ]
      if (isUserHeader) {
        ctas.unshift({ kind: 'rotateKey', label: 'Rotate key', variant: 'secondary' })
      }
      return ctas
    }

    case LinkState.DISABLED:
      return [
        { kind: 'enable', label: 'Enable', variant: 'primary' },
        { kind: 'disconnect', label: 'Disconnect', variant: 'danger' },
      ]

    case LinkState.AUTO_DISABLED:
      return [
        { kind: 'enable', label: 'Re-enable', variant: 'primary' },
        { kind: 'disconnect', label: 'Disconnect', variant: 'danger' },
      ]

    case LinkState.NEEDS_RELINK:
      return isUserHeader
        ? [
            { kind: 'submitKey', label: 'Re-enter API key', variant: 'primary' },
            { kind: 'disconnect', label: 'Disconnect', variant: 'danger' },
          ]
        : [
            { kind: 'connect', label: 'Reconnect', variant: 'primary' },
            { kind: 'disconnect', label: 'Disconnect', variant: 'danger' },
          ]

    default:
      return []
  }
}

// linkStateLabel maps the enum to a short, human-friendly badge
// string. Kept here so the UI never inlines magic enum-int strings.
export function linkStateLabel(state: LinkState): string {
  switch (state) {
    case LinkState.NONE:
      return 'not connected'
    case LinkState.CONNECTED:
      return 'connected'
    case LinkState.DISABLED:
      return 'disabled'
    case LinkState.AUTO_DISABLED:
      return 'auto-disabled'
    case LinkState.NEEDS_RELINK:
      return 'needs relink'
    default:
      return 'unknown'
  }
}

// linkStateTone returns a tailwind class fragment for the badge.
export function linkStateTone(state: LinkState): string {
  switch (state) {
    case LinkState.CONNECTED:
      return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200'
    case LinkState.AUTO_DISABLED:
    case LinkState.NEEDS_RELINK:
      return 'bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200'
    case LinkState.DISABLED:
      return 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200'
    case LinkState.NONE:
    default:
      return 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300'
  }
}
