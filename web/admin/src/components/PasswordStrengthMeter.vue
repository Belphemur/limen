<script setup lang="ts">
import { computed } from 'vue'

import {
  PASSWORD_MIN_SCORE,
  usePasswordStrength,
} from '@/composables/usePasswordStrength'

const props = defineProps<{
  password: string
  /** Low-entropy context (email, name, tenant) so zxcvbn penalises reuse. */
  userInputs?: string[]
}>()

defineEmits<{
  /** Fired whenever the score changes. Parent can gate submit on it. */
  (e: 'score', value: { score: 0 | 1 | 2 | 3 | 4; acceptable: boolean }): void
}>()

const passwordRef = computed(() => props.password)
const inputsRef = computed(() => props.userInputs ?? [])
const { score, warning, suggestions, crackTime, acceptable } = usePasswordStrength(
  passwordRef,
  inputsRef,
)

const SCORE_LABEL = ['Too weak', 'Weak', 'Fair', 'Good', 'Strong'] as const
const SCORE_COLOR = [
  'bg-danger',
  'bg-danger',
  'bg-warning',
  'bg-success',
  'bg-success',
] as const

const filled = computed(() => (props.password ? score.value + 1 : 0))
</script>

<template>
  <div>
    <div
      class="flex gap-1"
      role="progressbar"
      aria-label="Password strength"
      :aria-valuemin="0"
      :aria-valuemax="4"
      :aria-valuenow="score"
    >
      <span
        v-for="i in 5"
        :key="i"
        class="h-1.5 flex-1 rounded-full"
        :class="i <= filled ? SCORE_COLOR[score] : 'bg-surface-3'"
      />
    </div>
    <div class="mt-2 flex items-baseline justify-between gap-3">
      <span
        class="text-body-xs font-medium"
        :class="password ? (acceptable ? 'text-success' : 'text-text-muted') : 'text-text-muted'"
      >
        <template v-if="password">
          {{ SCORE_LABEL[score] }}
          <span v-if="!acceptable" class="text-text-muted"
            >&nbsp;— need “{{ SCORE_LABEL[PASSWORD_MIN_SCORE] }}”</span
          >
        </template>
        <template v-else> Password strength </template>
      </span>
      <span v-if="password && crackTime" class="text-body-xs text-text-muted">
        ~{{ crackTime }} to crack
      </span>
    </div>
    <div class="mt-1 h-32 text-body-xs">
      <p v-if="warning" class="text-warning">{{ warning }}</p>
      <ul v-if="suggestions.length" class="list-disc pl-5 text-text-muted">
        <li v-for="(s, i) in suggestions.slice(0, 2)" :key="i">{{ s }}</li>
      </ul>
    </div>
  </div>
</template>
