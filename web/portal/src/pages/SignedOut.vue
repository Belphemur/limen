<script setup lang="ts">
// Tenant-agnostic landing page Zitadel bounces back to after
// end-session. The portal cookie has already been cleared by
// /auth/logout before the browser was sent to Zitadel, and the
// router guard refused to render this route as part of a session
// flow — so by the time we get here there is no Pinia state worth
// resetting. The page is intentionally inert.
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
