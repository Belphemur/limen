// usePasswordStrength wraps zxcvbn-ts in a Vue 3 composable. The
// dictionaries are loaded once per page (setOptions is idempotent
// on subsequent calls) and scoring is reactive on the input ref.
//
// Score semantics (from zxcvbn): 0 = too guessable, 4 = strong.
// The signup wizard requires PASSWORD_MIN_SCORE before accepting
// the form.

import { computed, ref, watch, type Ref } from 'vue'
import { zxcvbn, zxcvbnOptions, type ZxcvbnResult } from '@zxcvbn-ts/core'
import * as zxcvbnCommonPackage from '@zxcvbn-ts/language-common'
import * as zxcvbnEnPackage from '@zxcvbn-ts/language-en'

export const PASSWORD_MIN_SCORE = 4

let optionsLoaded = false
function ensureOptions(): void {
  if (optionsLoaded) return
  zxcvbnOptions.setOptions({
    translations: zxcvbnEnPackage.translations,
    graphs: zxcvbnCommonPackage.adjacencyGraphs,
    dictionary: {
      ...zxcvbnCommonPackage.dictionary,
      ...zxcvbnEnPackage.dictionary,
    },
  })
  optionsLoaded = true
}

export interface PasswordStrength {
  score: Ref<0 | 1 | 2 | 3 | 4>
  /** zxcvbn warning string, e.g. "This is a top-10 common password." */
  warning: Ref<string>
  /** zxcvbn suggestions, e.g. "Add another word or two." */
  suggestions: Ref<string[]>
  /** Human-readable crack time at 10 guesses/sec (online throttled). */
  crackTime: Ref<string>
  /** True when score >= PASSWORD_MIN_SCORE. False for empty input. */
  acceptable: Ref<boolean>
}

/**
 * Reactively score password as the user types. inputs (e.g. email,
 * name) are passed to zxcvbn so they count as low-entropy material.
 */
export function usePasswordStrength(
  password: Ref<string>,
  userInputs: Ref<string[]> = ref([]),
): PasswordStrength {
  ensureOptions()

  const result = ref<ZxcvbnResult | null>(null)

  const recompute = (): void => {
    if (!password.value) {
      result.value = null
      return
    }
    result.value = zxcvbn(password.value, userInputs.value)
  }

  watch([password, userInputs], recompute, { immediate: true })

  return {
    score: computed(() => (result.value?.score ?? 0) as 0 | 1 | 2 | 3 | 4),
    warning: computed(() => result.value?.feedback.warning ?? ''),
    suggestions: computed(() => result.value?.feedback.suggestions ?? []),
    crackTime: computed(
      () => (result.value?.crackTimesDisplay.onlineThrottling100PerHour as string) ?? '',
    ),
    acceptable: computed(
      () => result.value !== null && result.value.score >= PASSWORD_MIN_SCORE,
    ),
  }
}
