// Shared CTA (Call-To-Action) decision logic for MCP upstream
// management. Both the Admin and Portal SPAs use these helpers to
// determine which action buttons to show for each upstream row.
//
// LinkState numeric values mirror the protobuf enum in
// proto/limen/portal/v1/portal.proto so this module has zero proto
// dependencies.
//
//   LINK_STATE_UNSPECIFIED = 0
//   LINK_STATE_NONE        = 1
//   LINK_STATE_CONNECTED   = 2
//   LINK_STATE_DISABLED    = 3
//   LINK_STATE_AUTO_DISABLED = 4
//   LINK_STATE_NEEDS_RELINK  = 5

/** Numeric LinkState constants matching protobuf enum limen.portal.v1.LinkState. */
export const LinkState = {
  UNSPECIFIED: 0,
  NONE: 1,
  CONNECTED: 2,
  DISABLED: 3,
  AUTO_DISABLED: 4,
  NEEDS_RELINK: 5,
} as const

export type LinkStateValue = (typeof LinkState)[keyof typeof LinkState]

/**
 * Minimal shape of the upstream metadata consumed by CTA logic.
 * Satisfied structurally by the protobuf-generated `UpstreamSummary`
 * from either the portal or admin SPA.
 */
export interface UpstreamSummaryLike {
  requiresLink: boolean
  strategyType: string
  strategySubMode: string
  linkState: LinkStateValue
  hasUserOverride: boolean
}

// CTAKind enumerates the actions the Upstreams page can offer per row.
//   connect / submitKey / rotateKey: start or rotate per-user creds
//   clearOverride: revert a user's static_header override back to
//     the shared fallback
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
export function upstreamCTAs(up: UpstreamSummaryLike): CTA[] {
  if (!up.requiresLink) return []
  if (up.strategyType === STATIC_HEADER) {
    return up.strategySubMode === 'override'
      ? staticHeaderOverrideCTAs(up)
      : staticHeaderSharedCTAs(up.linkState)
  }
  return defaultCTAs(up.linkState)
}

function staticHeaderSharedCTAs(state: LinkStateValue): CTA[] {
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

function staticHeaderOverrideCTAs(up: UpstreamSummaryLike): CTA[] {
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

function defaultCTAs(state: LinkStateValue): CTA[] {
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

// linkStateLabel maps the state to a short, human-friendly badge string.
export function linkStateLabel(state: LinkStateValue): string {
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

/**
 * Returns Tailwind utility classes for link state badge styling.
 * NOTE: These classes use the portal SPA's color palette (emerald/amber/slate).
 * The admin SPA uses its own `linkStatusPill()` with MD3 tokens instead.
 */
export function linkStateTone(state: LinkStateValue): string {
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
