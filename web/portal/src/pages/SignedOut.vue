<script setup lang="ts">
import { onMounted } from 'vue'
import { useSessionStore } from '@/stores/session'

// Tenant-agnostic landing page Zitadel bounces back to after end-session.
// The Limen portal session cookie has already been cleared by /auth/logout
// before the browser was sent to Zitadel, so by the time we render here
// the user is fully signed out on both sides. We still wipe the Pinia
// store explicitly: if the user hits Back after this page, the previous
// route should not see stale `authenticated=true`.
const session = useSessionStore()
onMounted(() => {
    session.reset()
})
</script>

<template>
    <div class="mx-auto mt-24 max-w-md text-center">
        <h1 class="text-2xl font-semibold">Signed out</h1>
        <p class="mt-2 text-sm text-slate-500">
            You've been signed out of Limen. Navigate to your tenant URL
            (<code>/t/&lt;tenant&gt;/</code>) to sign in again.
        </p>
    </div>
</template>
