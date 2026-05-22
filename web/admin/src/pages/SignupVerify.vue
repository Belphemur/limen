<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ConnectError, Code } from '@connectrpc/connect'

import PasswordStrengthMeter from '@/components/PasswordStrengthMeter.vue'
import { usePasswordStrength } from '@/composables/usePasswordStrength'
import { signupClient } from '@/transport/adminClient'

const route = useRoute()
const router = useRouter()
const status = ref<'verifying' | 'password' | 'completing' | 'redirecting' | 'error'>(
  'verifying',
)
const errorMsg = ref('')
const completionToken = ref('')
const password = ref('')
const passwordConfirm = ref('')
const PASSWORD_MIN_LEN = 8

const { acceptable: strengthOk } = usePasswordStrength(password)

async function verify(): Promise<void> {
  const token = typeof route.query.token === 'string' ? route.query.token : ''
  if (!token) {
    status.value = 'error'
    errorMsg.value = 'Missing verification token.'
    return
  }
  try {
    const res = await signupClient().verifyEmail({ token })
    completionToken.value = res.completionToken
    status.value = 'password'
  } catch (e) {
    status.value = 'error'
    errorMsg.value = explain(e, 'Verification link is invalid or has expired.')
  }
}

async function submitPassword(): Promise<void> {
  errorMsg.value = ''
  if (password.value.length < PASSWORD_MIN_LEN) {
    errorMsg.value = `Password must be at least ${PASSWORD_MIN_LEN} characters.`
    return
  }
  if (!strengthOk.value) {
    errorMsg.value = 'Please choose a stronger password.'
    return
  }
  if (password.value !== passwordConfirm.value) {
    errorMsg.value = 'Passwords do not match.'
    return
  }
  status.value = 'completing'
  try {
    const res = await signupClient().completeSignup({
      completionToken: completionToken.value,
      password: password.value,
    })
    status.value = 'redirecting'
    window.location.replace(res.redirectUrl)
  } catch (e) {
    status.value = 'password'
    errorMsg.value = explain(e, 'Could not complete signup. Please try again.')
  }
}

function explain(e: unknown, fallback: string): string {
  if (e instanceof ConnectError) {
    switch (e.code) {
      case Code.NotFound:
        return 'Verification link is invalid or has already been used.'
      case Code.DeadlineExceeded:
        return 'This verification link has expired. Please start over.'
      case Code.FailedPrecondition:
        return 'Please verify your email before completing signup.'
      case Code.Unauthenticated:
        return 'We could not find your signup session. Please start over.'
      case Code.InvalidArgument:
        return e.rawMessage || 'Please choose a stronger password.'
      default:
        return e.rawMessage || fallback
    }
  }
  return fallback
}

onMounted(verify)

function backToStart(): void {
  router.push('/signup')
}
</script>

<template>
  <main class="grid min-h-dvh place-items-center bg-bg p-6 text-text">
    <div class="w-full max-w-md rounded-lg bg-surface-1 p-8 shadow-soft">
      <template v-if="status === 'verifying'">
        <h1 class="text-display-2 font-display text-text text-center">Verifying email…</h1>
        <p class="mt-3 text-body-md text-text-muted text-center">
          Confirming your verification link.
        </p>
      </template>

      <template v-else-if="status === 'password' || status === 'completing'">
        <h1 class="text-display-2 font-display text-text">Set your password</h1>
        <p class="mt-2 text-body-sm text-text-muted">
          One last step. Choose a password to finish creating your tenant. You'll use it together
          with your email to sign in.
        </p>
        <form class="mt-6 space-y-4" @submit.prevent="submitPassword">
          <div>
            <label class="mb-1 block text-body-sm font-medium text-text" for="password"
              >Password</label
            >
            <input
              id="password"
              v-model="password"
              type="password"
              autocomplete="new-password"
              required
              :minlength="PASSWORD_MIN_LEN"
              :disabled="status === 'completing'"
              class="h-10 w-full rounded-md border border-divider bg-surface-2 px-3 text-body-md text-text outline-none focus:border-primary"
            />
          </div>
          <div>
            <label class="mb-1 block text-body-sm font-medium text-text" for="passwordConfirm"
              >Confirm password</label
            >
            <input
              id="passwordConfirm"
              v-model="passwordConfirm"
              type="password"
              autocomplete="new-password"
              required
              :minlength="PASSWORD_MIN_LEN"
              :disabled="status === 'completing'"
              class="h-10 w-full rounded-md border border-divider bg-surface-2 px-3 text-body-md text-text outline-none focus:border-primary"
            />
          </div>
          <PasswordStrengthMeter :password="password" />
          <p v-if="errorMsg" class="text-body-sm text-danger" role="alert">{{ errorMsg }}</p>
          <button
            type="submit"
            :disabled="status === 'completing' || !strengthOk || password !== passwordConfirm"
            class="inline-flex h-11 w-full items-center justify-center rounded-md bg-primary px-4 text-body-md font-semibold text-on-primary shadow-soft transition hover:bg-primary/90 disabled:opacity-60"
          >
            {{ status === 'completing' ? 'Provisioning…' : 'Finish signup' }}
          </button>
        </form>
      </template>

      <template v-else-if="status === 'redirecting'">
        <h1 class="text-display-2 font-display text-text text-center">Almost there</h1>
        <p class="mt-3 text-body-md text-text-muted text-center">
          Signing you in…
        </p>
      </template>

      <template v-else>
        <h1 class="text-display-2 font-display text-text text-center">Something went wrong</h1>
        <p class="mt-3 text-body-md text-danger text-center" role="alert">{{ errorMsg }}</p>
        <div class="mt-6 text-center">
          <button
            type="button"
            class="inline-flex h-10 items-center rounded-md bg-surface-2 px-4 text-body-sm font-medium text-text shadow-soft transition hover:bg-surface-3"
            @click="backToStart"
          >
            Back to signup
          </button>
        </div>
      </template>
    </div>
  </main>
</template>
