<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Server, Users, KeyRound, Code2, Copy, Share2 } from '@lucide/vue'
import { fetchDiscovery, useSessionStore } from '@limen/shared'
import { tenantPrefix } from '@limen/shared/session'
import { create } from '@bufbuild/protobuf'
import { adminClient, portalClient } from '@/transport/adminClient'
import { UpdateTenantSettingsRequestSchema } from '@/gen/limen/admin/v1/admin_pb.ts'
import { type UpstreamSummary } from '@/gen/limen/portal/v1/portal_pb.ts'
import { ROUTES } from '@/router/routes'
import SetupProgress from '@/components/SetupProgress.vue'
import OnboardingTaskCard from '@/components/OnboardingTaskCard.vue'
import SystemHealthEmpty from '@/components/SystemHealthEmpty.vue'
import QuickResources from '@/components/QuickResources.vue'
import BillingChart, { type BillingDataPoint } from '@/components/BillingChart.vue'

const router = useRouter()
const session = useSessionStore()

// Local mirror of the tenant-settings shape we actually render. The
// dashboard only needs the "was it ever set?" signal for each
// onboarding timestamp.
interface DashboardSettings {
  choseIde: boolean
  invitedTeam: boolean
  configured: boolean
}

const upstreams = ref<UpstreamSummary[]>([])
const settings = ref<DashboardSettings>({ choseIde: false, invitedTeam: false, configured: false })
const zitadelOrgId = ref('')
const issuer = ref('')
const hasActiveUserData = ref(false)
const hasSAConnectionData = ref(false)

const fetchActiveUserData = async (params: { from?: Date; to?: Date }) => {
  const resp = await adminClient().getActiveUserChart({
    fromDate: params.from
      ? { seconds: BigInt(Math.floor(params.from.getTime() / 1000)) }
      : undefined,
    toDate: params.to ? { seconds: BigInt(Math.floor(params.to.getTime() / 1000)) } : undefined,
  })
  return {
    hasData: resp.hasData,
    days: resp.days,
  }
}
const mapActiveUserData = (day: BillingDataPoint) => (day.activeUserCount as number) ?? 0

const fetchSAConnectionData = async (params: { from?: Date; to?: Date }) => {
  const resp = await adminClient().getSAConnectionChart({
    fromDate: params.from
      ? { seconds: BigInt(Math.floor(params.from.getTime() / 1000)) }
      : undefined,
    toDate: params.to ? { seconds: BigInt(Math.floor(params.to.getTime() / 1000)) } : undefined,
  })
  return {
    hasData: resp.hasData,
    days: resp.days,
  }
}

const mapSAConnectionData = (day: BillingDataPoint) => (day.peakConnections as number) ?? 0

onMounted(async () => {
  await Promise.all([
    session.refresh(),
    portalClient()
      .listUpstreams({})
      .then((r) => (upstreams.value = r.upstreams))
      .catch((err) => console.error('Failed to load upstreams:', err)),
    adminClient()
      .getTenantSettings({})
      .then((r) => {
        settings.value = {
          choseIde: (r.settings?.choseIdeAt ?? '') !== '',
          invitedTeam: (r.settings?.invitedTeamAt ?? '') !== '',
          configured: (r.settings?.configuredAt ?? '') !== '',
        }
        zitadelOrgId.value = r.zitadelOrgId
      })
      .catch((err) => console.error('Failed to load tenant settings:', err)),
    fetchDiscovery()
      .then((d) => (issuer.value = d.zitadelIssuer))
      .catch((err) => {
        console.error('Failed to fetch discovery:', err)
        issuer.value = ''
      }),
    // Billing chart data availability checks
    adminClient()
      .getActiveUserChart({})
      .then((r) => {
        hasActiveUserData.value = r.hasData
      })
      .catch((err) => {
        console.error('Failed to pre-check active user chart data availability:', err)
      }),
    adminClient()
      .getSAConnectionChart({})
      .then((r) => {
        hasSAConnectionData.value = r.hasData
      })
      .catch((err) => {
        console.error(
          'Failed to pre-check service account connection chart data availability:',
          err,
        )
      }),
  ])
})

interface Step {
  key: 'connect' | 'ide' | 'invite' | 'configure'
  done: boolean
}

const steps = computed<Step[]>(() => [
  {
    key: 'connect',
    done: upstreams.value.some((u) => u.tools.length > 0 && u.hasTenantLink),
  },
  { key: 'ide', done: settings.value.choseIde },
  { key: 'invite', done: settings.value.invitedTeam },
  { key: 'configure', done: settings.value.configured },
])

const completed = computed(() => steps.value.filter((s) => s.done).length)
const total = computed(() => steps.value.length)
const allDone = computed(() => total.value > 0 && completed.value === total.value)
const isDone = (key: Step['key']) => steps.value.find((s) => s.key === key)?.done ?? false

const firstName = computed(() => session.user?.firstName ?? 'there')

const portalUrl = computed(() => {
  const prefix = tenantPrefix() ?? ''
  return `${window.location.origin}${prefix}/portal/`
})
const portalCopied = ref(false)
async function copyPortalUrl() {
  try {
    await navigator.clipboard.writeText(portalUrl.value)
    portalCopied.value = true
    setTimeout(() => (portalCopied.value = false), 2000)
  } catch {
    portalCopied.value = false
  }
}

async function openMembers() {
  try {
    await adminClient().updateTenantSettings(
      create(UpdateTenantSettingsRequestSchema, { invitedTeamAtNow: true }),
    )
    settings.value.invitedTeam = true
    router.push(ROUTES.members)
  } catch (err) {
    console.error('Failed to mark members as opened:', err)
  }
}

async function openServiceAccounts() {
  try {
    await adminClient().updateTenantSettings(
      create(UpdateTenantSettingsRequestSchema, { configuredAtNow: true }),
    )
    settings.value.configured = true
    router.push(ROUTES.serviceAccounts)
  } catch (err) {
    console.error('Failed to mark service accounts as opened:', err)
  }
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

    <SetupProgress v-if="!allDone" :completed="completed" :total="total" />

    <!-- Task bento -->
    <section
      v-if="!allDone"
      class="grid gap-gutter md:grid-cols-2 xl:grid-cols-3"
      aria-label="Setup tasks"
    >
      <OnboardingTaskCard
        :icon="Server"
        title="Connect MCP Servers"
        body="Link your internal AI tools, external APIs, and custom data sources to the gateway."
        cta-label="Add First Server"
        :done="isDone('connect')"
        data-step="connect"
        @activate="router.push(ROUTES.mcpServerNew)"
      />
      <OnboardingTaskCard
        :icon="Users"
        title="Invite Your Team"
        body="Add collaborators to your organization to manage resources."
        cta-label="Manage Users"
        :done="isDone('invite')"
        data-step="invite"
        @activate="openMembers"
      />
      <OnboardingTaskCard
        :icon="Code2"
        title="Choose Your IDE"
        body="Pre-load the official redirect URIs for the AI IDE your users will connect from."
        cta-label="Pick IDEs"
        :done="isDone('ide')"
        data-step="ide"
        @activate="router.push(ROUTES.ideConfiguration)"
      />
      <OnboardingTaskCard
        :icon="KeyRound"
        title="Create Service Account"
        body="Generate an API token for cloud agents and CLI tools to access your gateway programmatically."
        cta-label="Set Up Service Account"
        :done="isDone('configure')"
        data-step="configure"
        @activate="openServiceAccounts"
      />
    </section>

    <!-- Portal URL share card: visible once the first MCP server is
         connected, since that's the point at which sharing the
         portal with end-users becomes useful. -->
    <section
      v-if="isDone('connect')"
      class="rounded-xl border border-outline-variant bg-surface-container-lowest p-5"
      data-testid="dashboard-portal-share"
    >
      <div class="flex items-start gap-3">
        <span
          class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-primary/10 text-primary"
        >
          <Share2 :size="18" aria-hidden="true" />
        </span>
        <div class="min-w-0 flex-1 space-y-2">
          <div>
            <h2 class="text-base font-semibold text-on-surface">Share with your team</h2>
            <p class="mt-0.5 text-sm text-on-surface-variant">
              Send this portal URL to your users. They sign in there to link their personal upstream
              accounts so the gateway can forward tool calls on their behalf.
            </p>
          </div>
          <div class="flex items-center gap-2">
            <a
              :href="portalUrl"
              target="_blank"
              rel="noopener"
              class="flex-1 truncate rounded border border-outline-variant bg-surface-variant px-3 py-2 font-mono text-sm text-primary underline decoration-dotted underline-offset-2 hover:text-primary-container"
              data-testid="dashboard-portal-url"
              >{{ portalUrl }}</a
            >
            <button
              type="button"
              class="inline-flex items-center gap-1 rounded border border-outline px-3 py-2 text-sm text-on-surface hover:bg-surface-variant"
              data-testid="dashboard-portal-copy"
              @click="copyPortalUrl"
            >
              <Copy :size="14" aria-hidden="true" />
              {{ portalCopied ? 'Copied' : 'Copy' }}
            </button>
          </div>
        </div>
      </div>
    </section>

    <!-- Usage charts: visible when billing data exists for this tenant -->
    <section v-if="hasActiveUserData || hasSAConnectionData" class="grid gap-gutter md:grid-cols-2">
      <BillingChart
        v-if="hasActiveUserData"
        title="Active Users"
        description="Distinct users who made tool calls each day"
        dataset-label="Active Users"
        line-color="var(--color-primary)"
        fill-color="rgba(38, 66, 230, 0.1)"
        :fetch-data-fn="fetchActiveUserData"
        :map-data-fn="mapActiveUserData"
      />
      <BillingChart
        v-if="hasSAConnectionData"
        title="SA Connections"
        description="Peak concurrent service account connections per day"
        dataset-label="Peak SA Connections"
        line-color="var(--color-tertiary)"
        fill-color="rgba(153, 60, 0, 0.1)"
        :fetch-data-fn="fetchSAConnectionData"
        :map-data-fn="mapSAConnectionData"
      />
    </section>
    <!-- Fallback: original bottom row when no usage data yet -->
    <section v-else class="grid gap-gutter md:grid-cols-2">
      <SystemHealthEmpty />
      <QuickResources />
    </section>
  </div>
</template>
