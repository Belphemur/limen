<script setup lang="ts">
// Presentational grid of Zitadel Console deep-links.
//
// Pure props in, <a> tags out. Owners (admin Members, future staff
// backoffice) supply the card list; this component owns the layout
// and the disabled state.

import type { Component } from 'vue'
import { ExternalLink } from '@lucide/vue'
import { zitadelConsoleUrl, type ZitadelView } from '../lib/zitadelConsole'

export interface ZitadelDirectoryCard {
  view: ZitadelView
  icon: Component
  title: string
  body: string
  ctaLabel?: string
}

defineProps<{
  issuer: string
  orgId: string
  cards: ZitadelDirectoryCard[]
}>()
</script>

<template>
  <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" data-testid="zitadel-directory">
    <article
      v-for="card in cards"
      :key="card.view + '-' + card.title"
      class="flex flex-col rounded-xl border border-border-subtle bg-surface-container-lowest p-5 shadow-sm"
    >
      <div
        class="mb-3 inline-flex h-10 w-10 items-center justify-center rounded-lg bg-primary/10 text-primary"
      >
        <component :is="card.icon" :size="20" aria-hidden="true" />
      </div>
      <h3 class="font-display text-base font-semibold text-on-surface">{{ card.title }}</h3>
      <p class="mt-1 flex-1 text-sm text-on-surface-variant">{{ card.body }}</p>
      <a
        :href="zitadelConsoleUrl(issuer, orgId, card.view) || '#'"
        target="_blank"
        rel="noopener noreferrer"
        :aria-disabled="!issuer || undefined"
        :tabindex="!issuer ? -1 : undefined"
        class="mt-4 inline-flex items-center gap-1.5 self-start rounded px-3 py-2 text-sm font-medium text-primary hover:underline"
        :class="{ 'pointer-events-none opacity-50': !issuer }"
        :data-testid="`zitadel-directory-${card.view}`"
      >
        {{ card.ctaLabel ?? 'Open in Zitadel Console' }}
        <ExternalLink :size="14" aria-hidden="true" />
      </a>
    </article>
  </div>
</template>
