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
    name: 'test',
    displayName: 'Test',
    mcpUrl: 'https://example.com/mcp',
    strategyType: 'mcp_spec',
    strategySubMode: '',
    requiresLink: true,
    linkState: LinkState.NONE,
    lastErrorReason: '',
    lastErrorAt: '',
    ...overrides,
  })
}

describe('upstreamCTAs', () => {
  it('returns no CTAs when the upstream does not require a link', () => {
    const cta = upstreamCTAs(summary({ requiresLink: false, strategyType: 'none' }))
    expect(cta).toEqual([])
  })

  it('returns no CTAs for static_header tenant-mode (tenant key is admin-managed)', () => {
    const cta = upstreamCTAs(
      summary({ strategyType: 'static_header', strategySubMode: 'tenant', requiresLink: false }),
    )
    expect(cta).toEqual([])
  })

  it('NONE + mcp_spec → Connect', () => {
    const cta = upstreamCTAs(summary({ linkState: LinkState.NONE }))
    expect(cta.map((c) => c.kind)).toEqual(['connect'])
  })

  it('NONE + static_header.user → Enter API key', () => {
    const cta = upstreamCTAs(
      summary({
        strategyType: 'static_header',
        strategySubMode: 'user',
        linkState: LinkState.NONE,
      }),
    )
    expect(cta.map((c) => c.kind)).toEqual(['submitKey'])
  })

  it('CONNECTED + mcp_spec → Disable + Disconnect', () => {
    const cta = upstreamCTAs(summary({ linkState: LinkState.CONNECTED }))
    expect(cta.map((c) => c.kind)).toEqual(['disable', 'disconnect'])
  })

  it('CONNECTED + static_header.user → Rotate + Disable + Disconnect', () => {
    const cta = upstreamCTAs(
      summary({
        strategyType: 'static_header',
        strategySubMode: 'user',
        linkState: LinkState.CONNECTED,
      }),
    )
    expect(cta.map((c) => c.kind)).toEqual(['rotateKey', 'disable', 'disconnect'])
  })

  it('DISABLED → Enable + Disconnect', () => {
    const cta = upstreamCTAs(summary({ linkState: LinkState.DISABLED }))
    expect(cta.map((c) => c.kind)).toEqual(['enable', 'disconnect'])
  })

  it('AUTO_DISABLED → Re-enable + Disconnect', () => {
    const cta = upstreamCTAs(summary({ linkState: LinkState.AUTO_DISABLED }))
    expect(cta.map((c) => c.kind)).toEqual(['enable', 'disconnect'])
    expect(cta[0].label).toBe('Re-enable')
  })

  it('NEEDS_RELINK + mcp_spec → Reconnect + Disconnect', () => {
    const cta = upstreamCTAs(summary({ linkState: LinkState.NEEDS_RELINK }))
    expect(cta.map((c) => c.kind)).toEqual(['connect', 'disconnect'])
    expect(cta[0].label).toBe('Reconnect')
  })

  it('NEEDS_RELINK + static_header.user → Re-enter API key + Disconnect', () => {
    const cta = upstreamCTAs(
      summary({
        strategyType: 'static_header',
        strategySubMode: 'user',
        linkState: LinkState.NEEDS_RELINK,
      }),
    )
    expect(cta.map((c) => c.kind)).toEqual(['submitKey', 'disconnect'])
    expect(cta[0].label).toBe('Re-enter API key')
  })
})
