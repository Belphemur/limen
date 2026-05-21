<script setup lang="ts">
// Members — Phase 9c slice 4.
//
// v1 is a deep-link card grid. Limen does not own member management;
// each card opens the right Zitadel Console view scoped to this
// tenant's org. v1.5 will add a read-only ListMembers table above
// this grid; v2 will own invites/role/remove via Zitadel Management
// API. See docs/phases/phase-09c-tenant-admin-spa.md.

import { onMounted, ref } from 'vue'
import { KeyRound, LockKeyhole, Palette, ShieldCheck, UserCog, UserPlus } from '@lucide/vue'
import { ZitadelDirectory, fetchDiscovery, type ZitadelDirectoryCard } from '@limen/shared'
import { adminClient } from '@/transport/adminClient'

const issuer = ref('')
const orgId = ref('')
const loading = ref(true)
const error = ref<string | null>(null)

const cards: ZitadelDirectoryCard[] = [
  {
    view: 'users',
    icon: UserPlus,
    title: 'Invite a member',
    body: 'Add a teammate to this organization. Zitadel sends the invite email and handles password / MFA enrollment.',
  },
  {
    view: 'users',
    icon: ShieldCheck,
    title: 'Manage roles & membership',
    body: 'Change a user’s role (owner / admin / member) or remove them from the organization. Same list as Invite.',
  },
  {
    view: 'idp',
    icon: KeyRound,
    title: 'Identity providers',
    body: 'Federate this organization to an external OIDC, SAML, or social IdP. Limen drives the standard OIDC flow; Zitadel renders the SSO buttons.',
  },
  {
    view: 'branding',
    icon: Palette,
    title: 'Branding',
    body: 'Customize the login screen logo, colors, and custom domain for this organization.',
  },
  {
    view: 'login-policy',
    icon: LockKeyhole,
    title: 'Login & lockout policy',
    body: 'Configure password rules, MFA enforcement, and lockout thresholds at the organization level.',
  },
  {
    view: 'profile',
    icon: UserCog,
    title: 'Your profile',
    body: 'Manage your own account: name, email, phone, language, and passkeys.',
  },
]

onMounted(async () => {
  try {
    const [disc, resp] = await Promise.all([
      fetchDiscovery().catch(() => ({ zitadelIssuer: '' })),
      adminClient().getTenantSettings({}),
    ])
    issuer.value = disc.zitadelIssuer
    orgId.value = resp.zitadelOrgId
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="space-y-6">
    <header>
      <h1 class="font-display text-3xl font-bold tracking-tight text-on-surface">
        Users &amp; Roles
      </h1>
      <p class="mt-2 max-w-2xl text-sm text-on-surface-variant">
        Member management lives in Zitadel Console at v1. Each card below opens the right Console
        view scoped to your organization.
      </p>
    </header>

    <ZitadelDirectory :issuer="issuer" :org-id="orgId" :cards="cards" />

    <p v-if="loading" class="text-sm text-on-surface-variant" data-testid="members-loading">
      Loading…
    </p>
    <p
      v-else-if="error"
      class="rounded border border-error/40 bg-error/10 p-3 text-sm text-error"
      data-testid="members-error"
    >
      Failed to load organization: {{ error }}
    </p>
  </div>
</template>
