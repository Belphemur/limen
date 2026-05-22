import { describe, expect, it } from 'vitest'
import { create, type MessageInitShape } from '@bufbuild/protobuf'
import {
  LinkState,
  UpstreamSummarySchema,
  type UpstreamSummary,
} from '@gen/limen/portal/v1/portal_pb.js'
import { upstreamCTAs } from './upstream-cta'

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

const sharedHeader = { strategyType: 'static_header', strategySubMode: 'shared' } as const
const overrideHeader = {
  strategyType: 'static_header',
  strategySubMode: 'override',
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

describe('upstreamCTAs — static_header shared', () => {
  it('NONE → Disable (creates a hide-only link row)', () => {
    expect(
      upstreamCTAs(summary({ ...sharedHeader, linkState: LinkState.NONE })).map((c) => c.kind),
    ).toEqual(['disable'])
  })
  it('CONNECTED → Disable + Disconnect', () => {
    expect(
      upstreamCTAs(summary({ ...sharedHeader, linkState: LinkState.CONNECTED })).map(
        (c) => c.kind,
      ),
    ).toEqual(['disable', 'disconnect'])
  })
  it('DISABLED → Enable + Disconnect', () => {
    expect(
      upstreamCTAs(summary({ ...sharedHeader, linkState: LinkState.DISABLED })).map((c) => c.kind),
    ).toEqual(['enable', 'disconnect'])
  })
})

describe('upstreamCTAs — static_header override (no user secret yet)', () => {
  it('NONE → Submit key', () => {
    expect(
      upstreamCTAs(
        summary({ ...overrideHeader, hasUserOverride: false, linkState: LinkState.NONE }),
      ).map((c) => c.kind),
    ).toEqual(['submitKey'])
  })
  it('CONNECTED (no override) → Submit key + Disable + Disconnect', () => {
    expect(
      upstreamCTAs(
        summary({ ...overrideHeader, hasUserOverride: false, linkState: LinkState.CONNECTED }),
      ).map((c) => c.kind),
    ).toEqual(['submitKey', 'disable', 'disconnect'])
  })
  it('DISABLED (no override) → Enable + Submit key', () => {
    expect(
      upstreamCTAs(
        summary({ ...overrideHeader, hasUserOverride: false, linkState: LinkState.DISABLED }),
      ).map((c) => c.kind),
    ).toEqual(['enable', 'submitKey'])
  })
})

describe('upstreamCTAs — static_header override (user secret set)', () => {
  it('CONNECTED → Rotate + Clear override + Disable + Disconnect', () => {
    const cta = upstreamCTAs(
      summary({ ...overrideHeader, hasUserOverride: true, linkState: LinkState.CONNECTED }),
    )
    expect(cta.map((c) => c.kind)).toEqual([
      'rotateKey',
      'clearOverride',
      'disable',
      'disconnect',
    ])
  })
  it('NEEDS_RELINK → Re-enter key + Clear override + Disable + Disconnect (tools still work via shared)', () => {
    const cta = upstreamCTAs(
      summary({ ...overrideHeader, hasUserOverride: true, linkState: LinkState.NEEDS_RELINK }),
    )
    expect(cta.map((c) => c.kind)).toEqual([
      'submitKey',
      'clearOverride',
      'disable',
      'disconnect',
    ])
    expect(cta[0].label).toBe('Re-enter API key')
  })
  it('AUTO_DISABLED → Re-enable + Clear override + Disconnect', () => {
    const cta = upstreamCTAs(
      summary({ ...overrideHeader, hasUserOverride: true, linkState: LinkState.AUTO_DISABLED }),
    )
    expect(cta.map((c) => c.kind)).toEqual(['enable', 'clearOverride', 'disconnect'])
    expect(cta[0].label).toBe('Re-enable')
  })
})
