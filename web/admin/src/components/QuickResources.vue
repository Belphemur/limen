<script setup lang="ts">
import { BookOpen, Code, MessagesSquare, ExternalLink } from '@lucide/vue'
import type { Component } from 'vue'

interface Resource {
  label: string
  description: string
  href: string
  icon: Component
}

const resources: Resource[] = [
  {
    label: 'Documentation',
    description: 'Guides, concepts, and how-to articles.',
    href: 'https://limen.dev/docs',
    icon: BookOpen,
  },
  {
    label: 'API Reference',
    description: 'Connect-RPC and MCP endpoint reference.',
    href: 'https://limen.dev/docs/api',
    icon: Code,
  },
  {
    label: 'Community Support',
    description: 'Ask questions and share patterns.',
    href: 'https://limen.dev/community',
    icon: MessagesSquare,
  },
]
</script>

<template>
  <section
    class="rounded-lg border border-border-subtle bg-surface p-6 shadow-sm"
    aria-labelledby="quick-resources-title"
  >
    <h3 id="quick-resources-title" class="font-display text-lg font-semibold text-on-surface">
      Quick Resources
    </h3>
    <ul class="mt-4 divide-y divide-border-subtle">
      <li v-for="r in resources" :key="r.href">
        <a
          :href="r.href"
          target="_blank"
          rel="noreferrer noopener"
          class="flex items-center gap-4 rounded-md px-2 py-3 transition-colors hover:bg-surface-container-low"
        >
          <span
            class="inline-flex h-9 w-9 items-center justify-center rounded-md bg-surface-container text-on-surface-variant"
          >
            <component :is="r.icon" :size="18" aria-hidden="true" />
          </span>
          <span class="flex-1">
            <span class="block text-sm font-medium text-on-surface">{{ r.label }}</span>
            <span class="block text-xs text-on-surface-variant">{{ r.description }}</span>
          </span>
          <ExternalLink :size="14" aria-hidden="true" class="text-on-surface-variant" />
        </a>
      </li>
    </ul>
  </section>
</template>
