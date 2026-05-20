<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Server, Users, Cog, ArrowRight, ExternalLink } from '@lucide/vue'
import { useSessionStore } from '@limen/shared/session'
import { ConnectError, Code } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import { adminClient, portalClient } from '@/transport/adminClient'
import { UpdateTenantSettingsRequestSchema } from '@/gen/limen/admin/v1/admin_pb.ts'
import { LinkState, type UpstreamSummary } from '@/gen/limen/portal/v1/portal_pb.ts'
import { ROUTES } from '@/router/routes'
import SetupProgress from '@/components/SetupProgress.vue'
import TaskBentoCard from '@/components/TaskBentoCard.vue'
import SystemHealthEmpty from '@/components/SystemHealthEmpty.vue'
import QuickResources from '@/components/QuickResources.vue'

const router = useRouter()
const session = useSessionStore()

// Local mirror of the tenant-settings shape we actually render. The
// generated admin TenantSettings carries protobuf Timestamp fields;
// we only need the "was it ever set?" signal here, so we collapse
// each timestamp to a boolean. Slice 3 will replace this with a real
// GetTenantSettings RPC.
interface DashboardSettings {
  invitedTeam: boolean
  configured: boolean
}

const upstreams = ref<UpstreamSummary[]>([])
const settings = ref<DashboardSettings>({ invitedTeam: false, configured: false })

function ignoreUnimplemented(err: unknown): void {
  // Slice 1 ships every admin RPC as Unimplemented. Treat that as
  // "no data yet" so the dashboard still renders. Anything else is
  // a real error and should bubble.
  if (err instanceof ConnectError && err.code === Code.Unimplemented) return
  throw err
}

onMounted(async () => {
  await Promise.all([
    session.refresh(),
    portalClient()
      .listUpstreams({})
      .then((r) => (upstreams.value = r.upstreams))
      .catch(ignoreUnimplemented),
  ])
})

interface Step {
  key: 'connect' | 'invite' | 'configure'
  done: boolean
}

const steps = computed<Step[]>(() => [
  {
    key: 'connect',
    // An upstream counts as "ready" once it has cached tools AND
    // either does not need linking or has been linked.
    done: upstreams.value.some(
      (u) =>
        u.tools.length > 0 &&
        (!u.requiresLink || u.linkState === LinkState.CONNECTED),
    ),
  },
  { key: 'invite', done: settings.value.invitedTeam },
  { key: 'configure', done: settings.value.configured },
])

const completed = computed(() => steps.value.filter((s) => s.done).length)
const total = computed(() => steps.value.length)
const isDone = (key: Step['key']) => steps.value.find((s) => s.key === key)?.done ?? false

const firstName = computed(() => session.user?.firstName ?? 'there')

async function openZitadelConsole() {
  // Optimistically tick the local "invited" flag so the step flips
  // even when slice 3's UpdateTenantSettings backend is still
  // Unimplemented. Once slice 3 lands the RPC response replaces this.
  settings.value.invitedTeam = true
  try {
    await adminClient().updateTenantSettings(
      create(UpdateTenantSettingsRequestSchema, { invitedTeamAtNow: true }),
    )
  } catch (err) {
    ignoreUnimplemented(err)
  }
  // TODO(phase-9c-slice-3): replace the hard-coded link with the
  // Zitadel issuer returned by GET /auth/discovery.
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
