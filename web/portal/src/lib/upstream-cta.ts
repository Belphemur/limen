import { LinkState, type UpstreamSummary } from '@gen/limen/portal/v1/portal_pb.js'

// CTAKind enumerates the actions the Upstreams page can offer per
// row.
//   connect / submitKey / rotateKey: start or rotate per-user creds
//   clearOverride: revert a user's static_header override back to
//     the shared fallback (Phase 9g)
//   enable / disable: flip the link's enabled flag
//   disconnect: drop the user's link row entirely
export type CTAKind =
  | 'connect'
  | 'submitKey'
  | 'rotateKey'
  | 'clearOverride'
  | 'enable'
  | 'disable'
  | 'disconnect'

export interface CTA {
  kind: CTAKind
  label: string
  variant: 'primary' | 'secondary' | 'danger'
}

const STATIC_HEADER = 'static_header'

// upstreamCTAs returns the ordered list of CTAs for a given upstream
// row. An empty list means "nothing for the user to do" — true for
// strategies that don't need a link at all (`none`).
//
// Decision table (Phase 9g, see docs/phases/phase-09g-static-header-rework.md):
//
//   strategy       sub_mode   override?  link_state      CTAs
//   none            -          -          -              []
//   static_header   shared     -          NONE           [Disable]
//   static_header   shared     -          CONNECTED      [Disable, Disconnect]
//   static_header   shared     -          DISABLED       [Enable, Disconnect]
//   static_header   shared     -          AUTO_DISABLED  [Re-enable, Disconnect]
//   static_header   override   no         NONE           [Submit key]
//   static_header   override   no         CONNECTED      [Submit key, Disable, Disconnect]
//   static_header   override   no         DISABLED       [Enable, Submit key]
//   static_header   override   no         AUTO_DISABLED  [Re-enable, Submit key]
//   static_header   override   yes        CONNECTED      [Rotate key, Clear override, Disable, Disconnect]
//   static_header   override   yes        NEEDS_RELINK   [Re-enter key, Clear override, Disable, Disconnect]
//   static_header   override   yes        DISABLED       [Enable, Clear override, Disconnect]
//   static_header   override   yes        AUTO_DISABLED  [Re-enable, Clear override, Disconnect]
//   mcp_spec        -          -          NONE           [Connect]
//   mcp_spec        -          -          CONNECTED      [Disable, Disconnect]
//   mcp_spec        -          -          DISABLED       [Enable, Disconnect]
//   mcp_spec        -          -          AUTO_DISABLED  [Re-enable, Disconnect]
//   mcp_spec        -          -          NEEDS_RELINK   [Reconnect, Disconnect]
export function upstreamCTAs(up: UpstreamSummary): CTA[] {
  if (!up.requiresLink) return []
  if (up.strategyType === STATIC_HEADER) {
    return up.strategySubMode === 'override'
      ? staticHeaderOverrideCTAs(up)
      : staticHeaderSharedCTAs(up.linkState)
  }
  return defaultCTAs(up.linkState)
}

function staticHeaderSharedCTAs(state: LinkState): CTA[] {
  switch (state) {
    case LinkState.NONE:
      return [disable]
    case LinkState.CONNECTED:
      return [disable, disconnect]
    case LinkState.DISABLED:
      return [enable, disconnect]
    case LinkState.AUTO_DISABLED:
      return [reEnable, disconnect]
    case LinkState.NEEDS_RELINK:
      // Shouldn't happen for shared-only (Headers never reports relink),
      // but if it leaked through let the user clear the row.
      return [disconnect]
    default:
      return []
  }
}

function staticHeaderOverrideCTAs(up: UpstreamSummary): CTA[] {
  if (!up.hasUserOverride) {
    switch (up.linkState) {
      case LinkState.NONE:
        return [submitKey]
      case LinkState.CONNECTED:
        return [submitKey, disable, disconnect]
      case LinkState.DISABLED:
        return [enable, submitKey]
      case LinkState.AUTO_DISABLED:
        return [reEnable, submitKey]
      default:
        return [submitKey]
    }
  }
  switch (up.linkState) {
    case LinkState.CONNECTED:
      return [rotateKey, clearOverride, disable, disconnect]
    case LinkState.NEEDS_RELINK:
      return [reSubmitKey, clearOverride, disable, disconnect]
    case LinkState.DISABLED:
      return [enable, clearOverride, disconnect]
    case LinkState.AUTO_DISABLED:
      return [reEnable, clearOverride, disconnect]
    default:
      return [rotateKey, clearOverride, disconnect]
  }
}

function defaultCTAs(state: LinkState): CTA[] {
  switch (state) {
    case LinkState.NONE:
      return [connect]
    case LinkState.CONNECTED:
      return [disable, disconnect]
    case LinkState.DISABLED:
      return [enable, disconnect]
    case LinkState.AUTO_DISABLED:
      return [reEnable, disconnect]
    case LinkState.NEEDS_RELINK:
      return [reconnect, disconnect]
    default:
      return []
  }
}

const connect: CTA = { kind: 'connect', label: 'Connect', variant: 'primary' }
const reconnect: CTA = { kind: 'connect', label: 'Reconnect', variant: 'primary' }
const submitKey: CTA = { kind: 'submitKey', label: 'Enter API key', variant: 'primary' }
const reSubmitKey: CTA = { kind: 'submitKey', label: 'Re-enter API key', variant: 'primary' }
const rotateKey: CTA = { kind: 'rotateKey', label: 'Rotate key', variant: 'secondary' }
const clearOverride: CTA = {
  kind: 'clearOverride',
  label: 'Use shared key',
  variant: 'secondary',
}
const enable: CTA = { kind: 'enable', label: 'Enable', variant: 'primary' }
const reEnable: CTA = { kind: 'enable', label: 'Re-enable', variant: 'primary' }
const disable: CTA = { kind: 'disable', label: 'Disable', variant: 'secondary' }
const disconnect: CTA = { kind: 'disconnect', label: 'Disconnect', variant: 'danger' }

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
