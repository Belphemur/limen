<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ConnectError, Code } from '@connectrpc/connect'
import { UserPlus, ShieldCheck, Network, Code as CodeIcon, MailCheck, Lock, KeyRound, Server } from '@lucide/vue'

import logoUrl from '@/assets/limen-logo.svg'
import { signupClient } from '@/transport/adminClient'

const form = ref({
  tenantName: '',
  ownerGivenName: '',
  ownerFamilyName: '',
  ownerEmail: '',
})
const submitting = ref(false)
const errorMsg = ref('')
const submitted = ref(false)
const captchaProvider = ref<'none' | 'hcaptcha' | 'turnstile'>('none')
const captchaSiteKey = ref('')
const captchaToken = ref('')

interface Discovery {
  zitadelIssuer: string
  captchaProvider: 'none' | 'hcaptcha' | 'turnstile'
  captchaSiteKey: string
}

onMounted(async () => {
  try {
    const res = await fetch('/auth/discovery', { credentials: 'include' })
    if (!res.ok) return
    const d = (await res.json()) as Discovery
    captchaProvider.value = d.captchaProvider ?? 'none'
    captchaSiteKey.value = d.captchaSiteKey ?? ''
  } catch {
    // Discovery is best-effort; dev bypass still works.
  }
})

async function onSubmit(): Promise<void> {
  errorMsg.value = ''
  if (
    !form.value.tenantName.trim() ||
    !form.value.ownerGivenName.trim() ||
    !form.value.ownerFamilyName.trim() ||
    !form.value.ownerEmail.trim()
  ) {
    errorMsg.value = 'Please fill in every field.'
    return
  }
  const token =
    captchaProvider.value === 'none' ? 'dev-captcha-bypass' : captchaToken.value.trim()
  if (!token) {
    errorMsg.value = 'Please complete the captcha challenge.'
    return
  }
  submitting.value = true
  try {
    await signupClient().startSignup({
      tenantName: form.value.tenantName.trim(),
      ownerEmail: form.value.ownerEmail.trim(),
      ownerGivenName: form.value.ownerGivenName.trim(),
      ownerFamilyName: form.value.ownerFamilyName.trim(),
      captchaToken: token,
    })
    submitted.value = true
  } catch (e) {
    if (e instanceof ConnectError) {
      if (e.code === Code.ResourceExhausted) {
        errorMsg.value = 'Too many signup attempts from this network. Try again in an hour.'
      } else if (e.code === Code.PermissionDenied) {
        errorMsg.value = 'Captcha verification failed. Please try again.'
      } else if (e.code === Code.Unavailable) {
        errorMsg.value = 'Signup is currently disabled.'
      } else {
        errorMsg.value = e.rawMessage || 'Could not start signup.'
      }
    } else {
      errorMsg.value = 'Could not start signup. Please try again.'
    }
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <main class="min-h-dvh bg-bg text-text">
    <header class="mx-auto flex max-w-6xl items-center justify-between gap-3 px-6 pt-8">
      <a href="/" class="flex items-center gap-3">
        <img
          :src="logoUrl"
          alt=""
          aria-hidden="true"
          width="40"
          height="40"
          class="h-10 w-10 rounded-md bg-primary-container"
        />
        <div class="flex flex-col leading-tight">
          <span class="font-display text-lg font-semibold text-text">Limen</span>
          <span class="text-body-xs text-text-muted">MCP gateway for teams</span>
        </div>
      </a>
      <a
        href="/auth/login"
        class="text-body-sm font-medium text-text-muted transition hover:text-text"
      >
        Already have an account? <span class="text-primary">Sign in</span>
      </a>
    </header>

    <div class="mx-auto grid max-w-6xl gap-10 px-6 py-10 lg:grid-cols-[1.05fr_1fr] lg:py-16">
      <!-- Left: what is Limen -->
      <section class="flex flex-col justify-center">
        <span
          class="inline-flex w-fit items-center gap-2 rounded-full bg-primary/10 px-3 py-1 text-body-xs font-medium text-primary"
        >
          <Server class="h-3.5 w-3.5" :stroke-width="2" />
          Centralized MCP gateway
        </span>
        <h1 class="mt-4 text-display-1 font-display text-text">
          One MCP endpoint for every AI tool your team uses.
        </h1>
        <p class="mt-4 text-body-md text-text-muted">
          Aggregate every upstream MCP server — GitHub, Linear, Notion, your own — behind a single
          tenant-scoped URL. Hand it to Cursor, Claude Desktop, or VS Code and keep one auth
          boundary, one audit log, and one place to revoke access.
        </p>
        <ul class="mt-8 space-y-4">
          <li class="flex gap-3">
            <span
              class="flex h-9 w-9 flex-none items-center justify-center rounded-full bg-primary/10 text-primary"
            >
              <Network class="h-4 w-4" :stroke-width="1.75" />
            </span>
            <div>
              <p class="text-body-sm font-medium text-text">Aggregate every upstream</p>
              <p class="text-body-sm text-text-muted">
                Mount any MCP server under one tenant URL. Your IDE only ever sees Limen.
              </p>
            </div>
          </li>
          <li class="flex gap-3">
            <span
              class="flex h-9 w-9 flex-none items-center justify-center rounded-full bg-primary/10 text-primary"
            >
              <ShieldCheck class="h-4 w-4" :stroke-width="1.75" />
            </span>
            <div>
              <p class="text-body-sm font-medium text-text">Scoped tokens, real isolation</p>
              <p class="text-body-sm text-text-muted">
                OIDC-backed sessions, per-tenant row-level isolation, and encrypted credentials at
                rest.
              </p>
            </div>
          </li>
          <li class="flex gap-3">
            <span
              class="flex h-9 w-9 flex-none items-center justify-center rounded-full bg-primary/10 text-primary"
            >
              <CodeIcon class="h-4 w-4" :stroke-width="1.75" />
            </span>
            <div>
              <p class="text-body-sm font-medium text-text">Compose tools in JavaScript</p>
              <p class="text-body-sm text-text-muted">
                Code Mode runs a sandboxed JS runtime so you can chain upstream tools server-side
                without a custom service.
              </p>
            </div>
          </li>
        </ul>
      </section>

      <!-- Right: signup card -->
      <section class="flex items-start justify-center">
        <div class="w-full max-w-md rounded-lg bg-surface-1 p-8 shadow-soft">
          <div v-if="!submitted">
            <div class="mb-2 flex items-center gap-3">
              <div
                class="flex h-10 w-10 items-center justify-center rounded-full bg-primary/10 text-primary"
              >
                <UserPlus class="h-5 w-5" :stroke-width="1.5" />
              </div>
              <h2 class="text-display-2 font-display text-text">Create your tenant</h2>
            </div>
            <p class="mb-6 text-body-sm text-text-muted">
              You'll be the owner. We'll email a verification link to finish — takes about a
              minute.
            </p>
            <form class="space-y-4" @submit.prevent="onSubmit">
              <div>
                <label class="mb-1 block text-body-sm font-medium text-text" for="tenantName"
                  >Tenant name</label
                >
                <input
                  id="tenantName"
                  v-model="form.tenantName"
                  type="text"
                  autocomplete="organization"
                  required
                  class="h-10 w-full rounded-md border border-divider bg-surface-2 px-3 text-body-md text-text outline-none focus:border-primary"
                />
              </div>
              <div class="grid grid-cols-2 gap-3">
                <div>
                  <label class="mb-1 block text-body-sm font-medium text-text" for="givenName"
                    >First name</label
                  >
                  <input
                    id="givenName"
                    v-model="form.ownerGivenName"
                    type="text"
                    autocomplete="given-name"
                    required
                    class="h-10 w-full rounded-md border border-divider bg-surface-2 px-3 text-body-md text-text outline-none focus:border-primary"
                  />
                </div>
                <div>
                  <label class="mb-1 block text-body-sm font-medium text-text" for="familyName"
                    >Last name</label
                  >
                  <input
                    id="familyName"
                    v-model="form.ownerFamilyName"
                    type="text"
                    autocomplete="family-name"
                    required
                    class="h-10 w-full rounded-md border border-divider bg-surface-2 px-3 text-body-md text-text outline-none focus:border-primary"
                  />
                </div>
              </div>
              <div>
                <label class="mb-1 block text-body-sm font-medium text-text" for="email"
                  >Email</label
                >
                <input
                  id="email"
                  v-model="form.ownerEmail"
                  type="email"
                  autocomplete="email"
                  required
                  class="h-10 w-full rounded-md border border-divider bg-surface-2 px-3 text-body-md text-text outline-none focus:border-primary"
                />
              </div>
              <div v-if="captchaProvider !== 'none'">
                <label class="mb-1 block text-body-sm font-medium text-text" for="captcha"
                  >Captcha token</label
                >
                <input
                  id="captcha"
                  v-model="captchaToken"
                  type="text"
                  required
                  class="h-10 w-full rounded-md border border-divider bg-surface-2 px-3 text-body-md text-text outline-none focus:border-primary"
                  :placeholder="`Paste ${captchaProvider} response`"
                />
                <p class="mt-1 text-body-xs text-text-muted">
                  Provider: <code>{{ captchaProvider }}</code> — site key
                  <code>{{ captchaSiteKey }}</code>
                </p>
              </div>
              <p v-if="errorMsg" class="text-body-sm text-danger" role="alert">{{ errorMsg }}</p>
              <button
                type="submit"
                :disabled="submitting"
                class="inline-flex h-11 w-full items-center justify-center rounded-md bg-primary px-4 text-body-md font-semibold text-on-primary shadow-soft transition hover:bg-primary/90 disabled:opacity-60"
              >
                {{ submitting ? 'Creating…' : 'Create my tenant' }}
              </button>
              <p
                class="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-body-xs text-text-muted"
              >
                <span class="inline-flex items-center gap-1">
                  <Lock class="h-3 w-3" :stroke-width="2" /> No credit card
                </span>
                <span aria-hidden="true">•</span>
                <span class="inline-flex items-center gap-1">
                  <KeyRound class="h-3 w-3" :stroke-width="2" /> We never see your password
                </span>
              </p>
              <p class="text-center text-body-xs text-text-muted">
                By signing up you agree to our
                <a href="/terms" class="text-primary hover:underline">Terms</a> and
                <a href="/privacy" class="text-primary hover:underline">Privacy Policy</a>.
              </p>
            </form>
          </div>
          <div v-else class="text-center">
            <div
              class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-primary/10 text-primary"
            >
              <MailCheck class="h-7 w-7" :stroke-width="1.5" />
            </div>
            <h2 class="text-display-2 font-display text-text">Check your inbox</h2>
            <p class="mt-3 text-body-md text-text-muted">
              If <strong>{{ form.ownerEmail }}</strong> can sign up, we just sent a verification
              link there. Click it to finish creating your tenant.
            </p>
            <p class="mt-4 text-body-xs text-text-muted">
              The link expires in 24 hours. Didn't get it? Check your spam folder or
              <a href="/signup" class="text-primary hover:underline">try again</a>.
            </p>
          </div>
        </div>
      </section>
    </div>

    <footer
      class="mx-auto flex max-w-6xl flex-col items-center justify-between gap-3 border-t border-divider px-6 py-6 text-body-xs text-text-muted sm:flex-row"
    >
      <p>&copy; Limen — open-source MCP gateway.</p>
      <div class="flex items-center gap-4">
        <a href="https://github.com/belphemur/limen" class="hover:text-text">GitHub</a>
        <a href="/docs" class="hover:text-text">Docs</a>
        <a href="/privacy" class="hover:text-text">Privacy</a>
        <a href="/terms" class="hover:text-text">Terms</a>
      </div>
    </footer>
  </main>
</template>
