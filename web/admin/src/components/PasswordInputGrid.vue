<script setup lang="ts">
import { ref, computed, watchEffect } from 'vue'
import PasswordStrengthMeter from './PasswordStrengthMeter.vue'
import { usePasswordStrength } from '@/composables/usePasswordStrength'
import type { PasswordComplexityRules } from '@/gen/limen/signup/v1/signup_pb'

const props = defineProps<{
    modelValue: string
    complexity?: PasswordComplexityRules
    disabled?: boolean
    userInputs?: string[]
}>()

const emit = defineEmits<{
    (e: 'update:modelValue', value: string): void
    (e: 'valid', value: boolean): void
}>()

const password = computed({
    get: () => props.modelValue,
    set: (val) => emit('update:modelValue', val),
})

const passwordConfirm = ref('')

const passwordRef = computed(() => password.value)
const { acceptable: strengthOk } = usePasswordStrength(passwordRef)

const rules = computed(() => {
    const req = props.complexity
    if (!req) return []
    const p = password.value

    return [
        {
            label: `At least ${req.minLength ?? 8} characters`,
            valid: p.length >= (req.minLength ?? 8),
            enabled: true,
        },
        {
            label: 'Contains an uppercase letter',
            valid: /[A-Z]/.test(p),
            enabled: req.requiresUppercase ?? false,
        },
        {
            label: 'Contains a lowercase letter',
            valid: /[a-z]/.test(p),
            enabled: req.requiresLowercase ?? false,
        },
        {
            label: 'Contains a number',
            valid: /[0-9]/.test(p),
            enabled: req.requiresNumber ?? false,
        },
        {
            label: 'Contains a special character/symbol',
            valid: /[^a-zA-Z0-9]/.test(p),
            enabled: req.requiresSymbol ?? false,
        },
    ].filter((r) => r.enabled)
})

const allRulesValid = computed(() => {
    if (!props.complexity) return true
    return rules.value.every((r) => r.valid)
})

// Parent-level validation flag
const isValid = computed(() => {
    const p = password.value
    const isMatch = p === passwordConfirm.value
    return p !== '' && isMatch && allRulesValid.value && strengthOk.value
})

watchEffect(() => {
    emit('valid', isValid.value)
})
</script>

<template>
    <div class="space-y-4">
        <div>
            <label class="mb-1 block text-body-sm font-medium text-text" for="password">Password</label>
            <input id="password" v-model="password" type="password" autocomplete="new-password" required
                :minlength="complexity?.minLength ?? 8" :disabled="disabled"
                class="h-10 w-full rounded-md border border-divider bg-surface-2 px-3 text-body-md text-text outline-none focus:border-primary disabled:opacity-60" />
        </div>

        <div>
            <label class="mb-1 block text-body-sm font-medium text-text" for="passwordConfirm">Confirm password</label>
            <input id="passwordConfirm" v-model="passwordConfirm" type="password" autocomplete="new-password" required
                :minlength="complexity?.minLength ?? 8" :disabled="disabled"
                class="h-10 w-full rounded-md border border-divider bg-surface-2 px-3 text-body-md text-text outline-none focus:border-primary disabled:opacity-60" />
        </div>

        <PasswordStrengthMeter :password="password" :user-inputs="userInputs" />

        <!-- Complexity rules -->
        <div v-if="complexity" class="space-y-1.5 rounded-md bg-surface-2 p-3.5 text-body-xs border border-divider">
            <span class="font-semibold text-text">Password complexity requirements:</span>
            <ul class="space-y-1 mt-1.5">
                <li v-for="r in rules" :key="r.label" class="flex items-center gap-2">
                    <span :class="r.valid ? 'text-success' : 'text-text-muted'">
                        <svg v-if="r.valid" class="h-4 w-4 shrink-0 fill-current text-success" viewBox="0 0 20 20">
                            <path fill-rule="evenodd"
                                d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.707-9.293a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l5-5z"
                                clip-rule="evenodd" />
                        </svg>
                        <span v-else class="inline-block h-1.5 w-1.5 rounded-full bg-divider mx-1.5" />
                    </span>
                    <span :class="r.valid ? 'text-success font-medium' : 'text-text-muted'">{{ r.label }}</span>
                </li>
            </ul>
        </div>
    </div>
</template>
