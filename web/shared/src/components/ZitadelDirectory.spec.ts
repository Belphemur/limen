import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { h } from 'vue'
import ZitadelDirectory, {
  type ZitadelDirectoryCard,
} from './ZitadelDirectory.vue'

const Icon = { render: () => h('svg') }

const cards: ZitadelDirectoryCard[] = [
  { view: 'users', icon: Icon, title: 'Invite', body: 'b1' },
  { view: 'idp', icon: Icon, title: 'IdP', body: 'b2' },
  { view: 'branding', icon: Icon, title: 'Branding', body: 'b3' },
]

describe('ZitadelDirectory', () => {
  it('renders one anchor per card with href from zitadelConsoleUrl', () => {
    const w = mount(ZitadelDirectory, {
      props: { issuer: 'https://idp.example', orgId: 'org_1', cards },
    })
    const anchors = w.findAll('a')
    expect(anchors).toHaveLength(3)
    expect(anchors[0].attributes('href')).toBe(
      'https://idp.example/ui/console/users?org=org_1',
    )
    expect(anchors[1].attributes("href")).toBe(
      "https://idp.example/ui/console/org-settings?id=idp&org=org_1",
    );
    expect(anchors[2].attributes("href")).toBe(
      "https://idp.example/ui/console/org-settings?id=branding&org=org_1",
    );
    for (const a of anchors) {
      expect(a.attributes('target')).toBe('_blank')
      expect(a.attributes('rel')).toBe('noopener noreferrer')
    }
  })

  it('disables every anchor when issuer is empty', () => {
    const w = mount(ZitadelDirectory, {
      props: { issuer: '', orgId: 'org_1', cards },
    })
    const anchors = w.findAll('a')
    for (const a of anchors) {
      expect(a.attributes('aria-disabled')).toBe('true')
      expect(a.attributes('tabindex')).toBe('-1')
      expect(a.classes()).toContain('pointer-events-none')
      expect(a.classes()).toContain('opacity-50')
    }
  })

  it('uses the card ctaLabel override when provided', () => {
    const w = mount(ZitadelDirectory, {
      props: {
        issuer: 'https://idp.example',
        orgId: 'org_1',
        cards: [{ ...cards[0], ctaLabel: 'Invite a teammate' }],
      },
    })
    expect(w.find('a').text()).toContain('Invite a teammate')
  })

  it('exposes a stable test id per view', () => {
    const w = mount(ZitadelDirectory, {
      props: { issuer: 'https://idp.example', orgId: 'org_1', cards },
    })
    expect(w.find('[data-testid="zitadel-directory-users"]').exists()).toBe(true)
    expect(w.find('[data-testid="zitadel-directory-idp"]').exists()).toBe(true)
    expect(w.find('[data-testid="zitadel-directory-branding"]').exists()).toBe(true)
  })
})
