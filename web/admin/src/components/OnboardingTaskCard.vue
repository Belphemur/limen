<script setup lang="ts">
import { ArrowRight } from '@lucide/vue'
import type { Component } from 'vue'
import TaskBentoCard from './TaskBentoCard.vue'

defineProps<{
  icon: Component
  title: string
  body: string
  ctaLabel: string
  done: boolean
  dataStep: string
}>()

defineEmits<{
  (e: 'activate'): void
}>()
</script>

<template>
  <TaskBentoCard
    variant="primary"
    :icon="icon"
    :title="title"
    :body="body"
    :cta-label="ctaLabel"
    :cta-icon="ArrowRight"
    :done="done"
    :data-step="dataStep"
    @activate="$emit('activate')"
  />
</template>
