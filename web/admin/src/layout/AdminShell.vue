<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterView } from 'vue-router'
import Sidebar from './Sidebar.vue'
import Topbar from './Topbar.vue'
import { useSessionStore } from '@limen/shared/session'

const session = useSessionStore()

onMounted(() => {
  void session.refresh()
})
</script>

<template>
  <div class="min-h-screen bg-bg-main text-on-surface">
    <a
      href="#main"
      class="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-toast focus:rounded focus:bg-primary focus:px-3 focus:py-2 focus:text-on-primary"
    >
      Skip to content
    </a>
    <Sidebar />
    <Topbar />
    <main id="main" class="ml-0 pt-header-height md:ml-sidebar-width">
      <div class="mx-auto w-full max-w-[1440px] p-4 md:p-container-padding">
        <RouterView />
      </div>
    </main>
  </div>
</template>
