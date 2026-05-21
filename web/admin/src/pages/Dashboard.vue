<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Server, Users, Cog, Code2, ArrowRight, ExternalLink, Copy, Share2 } from '@lucide/vue'
import { fetchDiscovery, useSessionStore, zitadelConsoleUrl } from '@limen/shared'
import { tenantPrefix } from '@limen/shared/session'
import { create } from '@bufbuild/protobuf'
import { adminClient, portalClient } from '@/transport/adminClient'
import {
  MarkIDEChoiceSkippedRequestSchema,
  UpdateTenantSettingsRequestSchema,
} from '@/gen/limen/admin/v1/admin_pb.ts'
import { LinkState, type UpstreamSummary } from '@/gen/limen/portal/v1/portal_pb.ts'
import { ROUTES } from '@/router/routes'
import SetupProgress from '@/components/SetupProgress.vue'
import TaskBentoCard from '@/components/TaskBentoCard.vue'
import SystemHealthEmpty from '@/components/SystemHealthEmpty.vue'
import QuickResources from '@/components/QuickResources.vue'

const router = useRouter()
const session = useSessionStore()

// Local mirror of the tenant-settings shape we actually render. The
// dashboard only needs the "was it ever set?" signal for each
// onboarding timestamp.
interface DashboardSettings {
  invitedTeam: boolean
  configured: boolean
  choseIde: boolean
}

const upstreams = ref<UpstreamSummary[]>([])
const settings = ref<DashboardSettings>({ invitedTeam: false, configured: false, choseIde: false })
const zitadelOrgId = ref('')
const issuer = ref('')

onMounted(async () => {
  await Promise.all([
    session.refresh(),
    portalClient()
      .listUpstreams({})
      .then((r) => (upstreams.value = r.upstreams)),
    adminClient()
      .getTenantSettings({})
      .then((r) => {
        settings.value = {
          invitedTeam: (r.settings?.invitedTeamAt ?? '') !== '',
          configured: (r.settings?.configuredAt ?? '') !== '',
          choseIde: (r.settings?.choseIdeAt ?? '') !== '',
        }
        zitadelOrgId.value = r.zitadelOrgId
      }),
    fetchDiscovery()
      .then((d) => (issuer.value = d.zitadelIssuer))
      .catch(() => (issuer.value = '')),
  ])
})

interface Step {
  key: 'connect' | 'invite' | 'configure' | 'ide'
  done: boolean
}

const steps = computed<Step[]>(() => [
  {
    key: 'connect',
    done: upstreams.value.some(
      (u) =>
        u.tools.length > 0 &&
        (!u.requiresLink || u.linkState === LinkState.CONNECTED),
    ),
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

async function openZitadelConsole() {
  const url = zitadelConsoleUrl(issuer.value, zitadelOrgId.value, 'users')
  try {
    const resp = await adminClient().updateTenantSettings(
      create(UpdateTenantSettingsRequestSchema, { invitedTeamAtNow: true }),
    )
    settings.value.invitedTeam = (resp.settings?.invitedTeamAt ?? '') !== ''
  } catch {
    settings.value.invitedTeam = true
  }
  if (url) {
    window.open(url, '_blank', 'noopener,noreferrer')
  }
}

async function skipIDEChoice() {
  try {
    const resp = await adminClient().markIDEChoiceSkipped(
      create(MarkIDEChoiceSkippedRequestSchema, {}),
    )
    settings.value.choseIde = (resp.settings?.choseIdeAt ?? '') !== ''
  } catch {
    settings.value.choseIde = true
  }
}

async function openSettings() {
  try {
    const resp = await adminClient().updateTenantSettings(
      create(UpdateTenantSettingsRequestSchema, { configuredAtNow: true }),
    )
    settings.value.configured = (resp.settings?.configuredAt ?? '') !== ''
  } catch {
    settings.value.configured = true
  }
  void router.push(ROUTES.settings)
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
    <section v-if="!allDone" class="grid gap-gutter md:grid-cols-2 xl:grid-cols-4" aria-label="Setup tasks">
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
      <div class="flex flex-col gap-2">
        <TaskBentoCard
          variant="secondary"
          :icon="Code2"
          title="Choose Your IDE"
          body="Pre-load the official redirect URIs for the AI IDE your users will connect from."
          cta-label="Pick IDEs"
          :done="isDone('ide')"
          data-step="ide"
          @activate="router.push(ROUTES.ideConfiguration)"
        />
        <button
          v-if="!isDone('ide')"
          type="button"
          class="self-start text-xs text-on-surface-variant underline hover:text-on-surface"
          data-testid="ide-skip"
          @click="skipIDEChoice"
        >
          Skip for now
        </button>
      </div>
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
        @activate="openSettings"
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
        <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-primary/10 text-primary">
          <Share2 :size="18" aria-hidden="true" />
        </span>
        <div class="min-w-0 flex-1 space-y-2">
          <div>
            <h2 class="text-base font-semibold text-on-surface">Share with your team</h2>
            <p class="mt-0.5 text-sm text-on-surface-variant">
              Send this portal URL to your users. They sign in there to link their
              personal upstream accounts so the gateway can forward tool calls on
              their behalf.
            </p>
          </div>
          <div class="flex items-center gap-2">
            <a
              :href="portalUrl"
              target="_blank"
              rel="noopener"
              class="flex-1 truncate rounded border border-outline-variant bg-surface-variant px-3 py-2 font-mono text-sm text-primary underline decoration-dotted underline-offset-2 hover:text-primary-container"
              data-testid="dashboard-portal-url"
            >{{ portalUrl }}</a>
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

    <!-- Bottom row -->
    <section class="grid gap-gutter md:grid-cols-2">
      <SystemHealthEmpty />
      <QuickResources />
    </section>
  </div>
</template>
