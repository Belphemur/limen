<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Server, Users, Cog, ArrowRight, ExternalLink } from '@lucide/vue'
import { useSessionStore } from '@limen/shared/session'
import { adminClient, type TenantSettings, type UpstreamRow } from '@/transport/adminClient'
import { ROUTES } from '@/router/routes'
import SetupProgress from '@/components/SetupProgress.vue'
import TaskBentoCard from '@/components/TaskBentoCard.vue'
import SystemHealthEmpty from '@/components/SystemHealthEmpty.vue'
import QuickResources from '@/components/QuickResources.vue'

const router = useRouter()
const session = useSessionStore()
const client = adminClient()

const upstreams = ref<UpstreamRow[]>([])
const settings = ref<TenantSettings>({ name: '', invitedTeamAt: null, configuredAt: null })

onMounted(async () => {
  await Promise.all([
    session.refresh(),
    client.listUpstreams().then((r) => (upstreams.value = r.upstreams)),
    client.getTenantSettings().then((r) => (settings.value = r)),
  ])
})

interface Step {
  key: 'connect' | 'invite' | 'configure'
  done: boolean
}

const steps = computed<Step[]>(() => [
  {
    key: 'connect',
    done: upstreams.value.some((u) => u.status === 'ready' && u.toolCount > 0),
  },
  { key: 'invite', done: settings.value.invitedTeamAt !== null },
  { key: 'configure', done: settings.value.configuredAt !== null },
])

const completed = computed(() => steps.value.filter((s) => s.done).length)
const total = computed(() => steps.value.length)
const isDone = (key: Step['key']) => steps.value.find((s) => s.key === key)?.done ?? false

const firstName = computed(() => session.user?.firstName ?? 'there')

async function openZitadelConsole() {
  // The real issuer comes from GET /auth/discovery; mock until the
  // backend handler lands. We still flip the invited_team marker so
  // the step ticks regardless of whether the new tab actually opens.
  // TODO(phase-9c-proto): swap the placeholder URL for the resolved
  //   Zitadel Console deep-link once /auth/discovery is wired.
  await client.markInvitedTeam()
  settings.value.invitedTeamAt = new Date().toISOString()
  window.open('https://zitadel.example/ui/console/users', '_blank', 'noopener,noreferrer')
}
</script>

<template>
  <div class="space-y-stack-lg">
    <!-- Welcome -->
    <header>
      <h1 class="font-display text-3xl font-bold tracking-tight text-on-surface">
        Welcome to Limen, {{ firstName }}
      </h1>
      <p class="mt-2 max-w-2xl text-sm text-on-surface-variant">
        Get started with your AI Gateway. Complete the steps below to fully configure your
        organization's environment and connect your first MCP servers.
      </p>
    </header>

    <SetupProgress :completed="completed" :total="total" />

    <!-- Task bento -->
    <section class="grid gap-gutter md:grid-cols-3" aria-label="Setup tasks">
      <div class="md:col-span-2">
        <TaskBentoCard
          variant="primary"
          :icon="Server"
          title="Connect MCP Servers"
          body="Link your internal AI tools, external APIs, and custom data sources to the gateway."
          cta-label="Add First Server"
          :cta-icon="ArrowRight"
          :done="isDone('connect')"
          data-step="connect"
          @activate="router.push(ROUTES.mcpServerNew)"
        />
      </div>
      <div class="flex flex-col gap-gutter">
        <TaskBentoCard
          variant="secondary"
          :icon="Users"
          title="Invite Your Team"
          body="Add collaborators to your organization to manage resources."
          cta-label="Manage Users in Zitadel"
          :cta-icon="ExternalLink"
          :done="isDone('invite')"
          data-step="invite"
          @activate="openZitadelConsole"
        />
        <TaskBentoCard
          variant="secondary"
          :icon="Cog"
          title="Configure Organization"
          body="Set up your tenant details, limits, and core preferences."
          cta-label="Review Settings"
          :done="isDone('configure')"
          data-step="configure"
          @activate="router.push(ROUTES.settings)"
        />
      </div>
    </section>

    <!-- Bottom row -->
    <section class="grid gap-gutter md:grid-cols-2">
      <SystemHealthEmpty />
      <QuickResources />
    </section>
  </div>
</template>
