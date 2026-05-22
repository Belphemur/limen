<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ConnectError, Code } from '@connectrpc/connect'

import { signupClient } from '@/transport/adminClient'

const route = useRoute()
const router = useRouter()
const status = ref<'verifying' | 'completing' | 'redirecting' | 'error'>('verifying')
const errorMsg = ref('')

async function run(): Promise<void> {
  const token = typeof route.query.token === 'string' ? route.query.token : ''
  if (!token) {
    status.value = 'error'
    errorMsg.value = 'Missing verification token.'
    return
  }
  let completionToken: string
  try {
    const res = await signupClient().verifyEmail({ token })
    completionToken = res.completionToken
  } catch (e) {
    status.value = 'error'
    errorMsg.value = explain(e, 'Verification link is invalid or has expired.')
    return
  }
  status.value = 'completing'
  try {
    const res = await signupClient().completeSignup({ completionToken })
    status.value = 'redirecting'
    window.location.replace(res.passwordInitUrl)
  } catch (e) {
    status.value = 'error'
    errorMsg.value = explain(e, 'Could not complete signup. Please try the link again.')
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
      default:
        return e.rawMessage || fallback
    }
  }
  return fallback
}

onMounted(run)

function backToStart(): void {
  router.push('/signup')
}
</script>

<template>
  <main class="grid min-h-dvh place-items-center bg-bg p-6 text-text">
    <div class="w-full max-w-md rounded-lg bg-surface-1 p-8 text-center shadow-soft">
      <template v-if="status === 'verifying' || status === 'completing'">
        <h1 class="text-display-2 font-display text-text">Finishing setup…</h1>
        <p class="mt-3 text-body-md text-text-muted">
          {{
            status === 'verifying'
              ? 'Verifying your email address.'
              : 'Provisioning your tenant.'
          }}
        </p>
      </template>
      <template v-else-if="status === 'redirecting'">
        <h1 class="text-display-2 font-display text-text">Almost there</h1>
        <p class="mt-3 text-body-md text-text-muted">
          Redirecting you to set your password…
        </p>
      </template>
      <template v-else>
        <h1 class="text-display-2 font-display text-text">Something went wrong</h1>
        <p class="mt-3 text-body-md text-danger" role="alert">{{ errorMsg }}</p>
        <button
          type="button"
          class="mt-6 inline-flex h-10 items-center rounded-md bg-surface-2 px-4 text-body-sm font-medium text-text shadow-soft transition hover:bg-surface-3"
          @click="backToStart"
        >
          Back to signup
        </button>
      </template>
    </div>
  </main>
</template>
