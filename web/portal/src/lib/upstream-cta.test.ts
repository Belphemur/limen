import { describe, expect, it } from 'vitest'
import { create, type MessageInitShape } from '@bufbuild/protobuf'
import {
  LinkState,
  UpstreamSummarySchema,
  type UpstreamSummary,
} from '@gen/limen/portal/v1/portal_pb.js'
import { upstreamCTAs } from '@limen/shared'

function summary(overrides: MessageInitShape<typeof UpstreamSummarySchema>): UpstreamSummary {
  return create(UpstreamSummarySchema, {
    publicId: 'up_test',
    identifier: 'test',
    displayName: 'Test',
    mcpUrl: 'https://example.com/mcp',
    strategyType: 'mcp_spec',
    strategySubMode: '',
    requiresLink: true,
    linkState: LinkState.NONE,
    hasUserOverride: false,
    lastErrorReason: '',
    lastErrorAt: '',
    ...overrides,
  })
}

const tenantOwnerHeader = {
  strategyType: 'static_header',
  strategySubMode: 'tenant_owner',
} as const
const byokHeader = {
  strategyType: 'static_header',
  strategySubMode: 'byok',
} as const

describe('upstreamCTAs — non-link strategies', () => {
  it('returns no CTAs when the upstream does not require a link', () => {
    const cta = upstreamCTAs(summary({ requiresLink: false, strategyType: 'none' }))
    expect(cta).toEqual([])
  })
})

describe('upstreamCTAs — mcp_spec', () => {
  it('NONE → Connect', () => {
    expect(upstreamCTAs(summary({ linkState: LinkState.NONE })).map((c) => c.kind)).toEqual([
      'connect',
    ])
  })
  it('CONNECTED → Disable + Disconnect', () => {
    expect(upstreamCTAs(summary({ linkState: LinkState.CONNECTED })).map((c) => c.kind)).toEqual([
      'disable',
      'disconnect',
    ])
  })
  it('AUTO_DISABLED → Re-enable + Disconnect', () => {
    const cta = upstreamCTAs(summary({ linkState: LinkState.AUTO_DISABLED }))
    expect(cta.map((c) => c.kind)).toEqual(['enable', 'disconnect'])
    expect(cta[0].label).toBe('Re-enable')
  })
  it('NEEDS_RELINK → Reconnect + Disconnect', () => {
    const cta = upstreamCTAs(summary({ linkState: LinkState.NEEDS_RELINK }))
    expect(cta.map((c) => c.kind)).toEqual(['connect', 'disconnect'])
    expect(cta[0].label).toBe('Reconnect')
  })
})

describe('upstreamCTAs — static_header tenant_owner', () => {
  it('NONE → Disable (creates a hide-only link row)', () => {
    expect(
      upstreamCTAs(summary({ ...tenantOwnerHeader, linkState: LinkState.NONE })).map((c) => c.kind),
    ).toEqual(['disable'])
  })
  it('CONNECTED → Disable + Disconnect', () => {
    expect(
      upstreamCTAs(summary({ ...tenantOwnerHeader, linkState: LinkState.CONNECTED })).map(
        (c) => c.kind,
      ),
    ).toEqual(['disable', 'disconnect'])
  })
  it('DISABLED → Enable + Disconnect', () => {
    expect(
      upstreamCTAs(summary({ ...tenantOwnerHeader, linkState: LinkState.DISABLED })).map(
        (c) => c.kind,
      ),
    ).toEqual(['enable', 'disconnect'])
  })
})

describe('upstreamCTAs — static_header byok (no user secret yet)', () => {
  it('NONE → Submit key', () => {
    expect(
      upstreamCTAs(
        summary({ ...byokHeader, hasUserOverride: false, linkState: LinkState.NONE }),
      ).map((c) => c.kind),
    ).toEqual(['submitKey'])
  })
  it('CONNECTED (no key) → Submit key + Disable + Disconnect', () => {
    expect(
      upstreamCTAs(
        summary({ ...byokHeader, hasUserOverride: false, linkState: LinkState.CONNECTED }),
      ).map((c) => c.kind),
    ).toEqual(['submitKey', 'disable', 'disconnect'])
  })
  it('DISABLED (no key) → Enable + Submit key', () => {
    expect(
      upstreamCTAs(
        summary({ ...byokHeader, hasUserOverride: false, linkState: LinkState.DISABLED }),
      ).map((c) => c.kind),
    ).toEqual(['enable', 'submitKey'])
  })
})

describe('upstreamCTAs — static_header byok (user secret set)', () => {
  it('CONNECTED → Rotate + Disable + Disconnect', () => {
    const cta = upstreamCTAs(
      summary({ ...byokHeader, hasUserOverride: true, linkState: LinkState.CONNECTED }),
    )
    expect(cta.map((c) => c.kind)).toEqual(['rotateKey', 'disable', 'disconnect'])
  })
  it('NEEDS_RELINK → Re-enter key + Disable + Disconnect', () => {
    const cta = upstreamCTAs(
      summary({ ...byokHeader, hasUserOverride: true, linkState: LinkState.NEEDS_RELINK }),
    )
    expect(cta.map((c) => c.kind)).toEqual(['submitKey', 'disable', 'disconnect'])
    expect(cta[0].label).toBe('Re-enter API key')
  })
  it('AUTO_DISABLED → Re-enable + Disconnect', () => {
    const cta = upstreamCTAs(
      summary({ ...byokHeader, hasUserOverride: true, linkState: LinkState.AUTO_DISABLED }),
    )
    expect(cta.map((c) => c.kind)).toEqual(['enable', 'disconnect'])
    expect(cta[0].label).toBe('Re-enable')
  })
})
