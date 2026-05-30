<script setup lang="ts">
// Members — Phase 9c slice 5.
//
// Limen-owned tenant member management. The full CRUD surface
// (List/Invite/UpdateRole/Remove) is a direct pass-through to Zitadel
// via AdminService; Limen never persists user identity.
//
// Below the table, an "Identity & policies" disclosure exposes the
// permanent Zitadel Console deep-links Limen will never own (IdP,
// branding, login/lockout policy, own profile, role assignment).

import { computed, onMounted, ref, watch } from 'vue'
import { ConnectError } from '@connectrpc/connect'
import { create } from '@bufbuild/protobuf'
import {
  ChevronDown,
  KeyRound,
  Loader2,
  LockKeyhole,
  Mail,
  Palette,
  Pencil,
  Search,
  Shield,
  ShieldCheck,
  Trash2,
  User,
  UserCog,
  UserPlus,
  X,
} from '@lucide/vue'
import {
  ErrorModal,
  ZitadelDirectory,
  fetchDiscovery,
  type ZitadelDirectoryCard,
} from '@limen/shared'
import { adminClient } from '@/transport/adminClient'
import {
  InviteMemberRequestSchema,
  ListMembersRequestSchema,
  MemberRole,
  MemberState,
  RemoveMemberRequestSchema,
  UpdateMemberRoleRequestSchema,
  type Member,
} from '@/gen/limen/admin/v1/admin_pb.ts'

const issuer = ref('')
const orgId = ref('')
const members = ref<Member[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const mutating = ref(false)
const mutationError = ref<string | null>(null)

const searchQuery = ref('')
const roleFilter = ref<MemberRole>(MemberRole.UNSPECIFIED)

// Modals.
const inviteOpen = ref(false)
const inviteForm = ref({ email: '', givenName: '', familyName: '', role: MemberRole.MEMBER })
const inviteError = ref<string | null>(null)

const editing = ref<Member | null>(null)
const editingRole = ref<MemberRole>(MemberRole.MEMBER)

const removingConfirm = ref<Member | null>(null)

// Last-owner guard: the org must always have ≥1 owner. Backend enforces
// the invariant; surfacing it client-side keeps the UI honest before
// the round-trip.
const ownerCount = computed(() => members.value.filter((m) => m.role === MemberRole.OWNER).length)
function isLastOwner(m: Member): boolean {
  return m.role === MemberRole.OWNER && ownerCount.value <= 1
}
const editingDemotesLastOwner = computed(() => {
  if (!editing.value) return false
  return isLastOwner(editing.value) && editingRole.value !== MemberRole.OWNER
})

const ROLE_LABELS: Record<MemberRole, string> = {
  [MemberRole.UNSPECIFIED]: 'No role',
  [MemberRole.MEMBER]: 'Member',
  [MemberRole.ADMIN]: 'Admin',
  [MemberRole.OWNER]: 'Owner',
}

const STATE_LABELS: Record<MemberState, string> = {
  [MemberState.UNSPECIFIED]: 'Unknown',
  [MemberState.ACTIVE]: 'Active',
  [MemberState.INACTIVE]: 'Inactive',
  [MemberState.LOCKED]: 'Locked',
  [MemberState.INITIAL]: 'Invited',
}

const STATE_CLASS: Record<MemberState, string> = {
  [MemberState.UNSPECIFIED]: 'bg-surface-variant text-on-surface-variant',
  [MemberState.ACTIVE]: 'bg-green-500/15 text-green-600 dark:text-green-400',
  [MemberState.INACTIVE]: 'bg-surface-variant text-on-surface-variant',
  [MemberState.LOCKED]: 'bg-error/15 text-error',
  [MemberState.INITIAL]: 'bg-amber-500/15 text-amber-600 dark:text-amber-400',
}

function roleIcon(role: MemberRole) {
  switch (role) {
    case MemberRole.OWNER:
      return ShieldCheck
    case MemberRole.ADMIN:
      return Shield
    case MemberRole.MEMBER:
      return User
    default:
      return User
  }
}

function initials(m: Member): string {
  const name = m.displayName || m.email
  const parts = name.trim().split(/\s+/)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return name.slice(0, 2).toUpperCase()
}

function formatRelative(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const diffMs = Date.now() - d.getTime()
  const min = Math.floor(diffMs / 60000)
  if (min < 1) return 'just now'
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const day = Math.floor(hr / 24)
  if (day < 30) return `${day}d ago`
  return d.toLocaleDateString()
}

const directoryCards = computed<ZitadelDirectoryCard[]>(() => [
  {
    view: 'idp',
    icon: KeyRound,
    title: 'Identity providers',
    body: 'Federate this organization to an external OIDC, SAML, or social IdP. Limen drives the OIDC flow; Zitadel renders the SSO buttons.',
  },
  {
    view: 'branding',
    icon: Palette,
    title: 'Branding',
    body: 'Customize the login screen logo, colors, and custom domain.',
  },
  {
    view: 'login',
    icon: LockKeyhole,
    title: 'Login & lockout',
    body: 'Configure password rules, MFA enforcement, and lockout thresholds at the organization level.',
  },
  {
    view: 'profile',
    icon: UserCog,
    title: 'Your profile',
    body: 'Manage your own account: name, email, phone, language, and passkeys.',
  },
])

let debounceHandle: ReturnType<typeof setTimeout> | null = null

async function loadMembers() {
  loading.value = true
  error.value = null
  try {
    const resp = await adminClient().listMembers(
      create(ListMembersRequestSchema, {
        search: searchQuery.value.trim(),
        roleFilter: roleFilter.value,
      }),
    )
    members.value = resp.members
  } catch (e) {
    error.value = e instanceof ConnectError ? e.message : String(e)
    members.value = []
  } finally {
    loading.value = false
  }
}

function onSearchInput() {
  if (debounceHandle) clearTimeout(debounceHandle)
  debounceHandle = setTimeout(() => {
    void loadMembers()
  }, 200)
}

watch(roleFilter, () => {
  void loadMembers()
})

onMounted(async () => {
  try {
    const [disc, settings] = await Promise.all([
      fetchDiscovery().catch(() => ({ zitadelIssuer: '' })),
      adminClient().getTenantSettings({}),
    ])
    issuer.value = disc.zitadelIssuer
    orgId.value = settings.zitadelOrgId
  } catch {
    // best-effort; the deep-link cards just won't carry an org scope.
  }
  await loadMembers()
})

function openInvite() {
  inviteForm.value = {
    email: '',
    givenName: '',
    familyName: '',
    role: MemberRole.MEMBER,
  }
  inviteError.value = null
  inviteOpen.value = true
}

async function submitInvite() {
  inviteError.value = null
  if (!inviteForm.value.email.trim()) {
    inviteError.value = 'Email is required.'
    return
  }
  mutating.value = true
  try {
    const resp = await adminClient().inviteMember(
      create(InviteMemberRequestSchema, {
        email: inviteForm.value.email.trim(),
        givenName: inviteForm.value.givenName.trim(),
        familyName: inviteForm.value.familyName.trim(),
        role: inviteForm.value.role,
      }),
    )
    if (resp.member) members.value = [resp.member, ...members.value]
    inviteOpen.value = false
  } catch (e) {
    inviteError.value = e instanceof ConnectError ? e.message : String(e)
  } finally {
    mutating.value = false
  }
}

function openEdit(m: Member) {
  editing.value = m
  editingRole.value = m.role === MemberRole.UNSPECIFIED ? MemberRole.MEMBER : m.role
  mutationError.value = null
}

function closeInvite() {
  inviteOpen.value = false
  inviteError.value = null
}

function closeEdit() {
  editing.value = null
  mutationError.value = null
}

function closeRemove() {
  removingConfirm.value = null
  mutationError.value = null
}

async function submitEdit() {
  if (!editing.value) return
  mutating.value = true
  mutationError.value = null
  try {
    await adminClient().updateMemberRole(
      create(UpdateMemberRoleRequestSchema, {
        userId: editing.value.userId,
        role: editingRole.value,
      }),
    )
    const m = members.value.find((x) => x.userId === editing.value!.userId)
    if (m) m.role = editingRole.value
    editing.value = null
  } catch (e) {
    mutationError.value = e instanceof ConnectError ? e.message : String(e)
  } finally {
    mutating.value = false
  }
}

function askRemove(m: Member) {
  removingConfirm.value = m
  mutationError.value = null
}

async function confirmRemove() {
  if (!removingConfirm.value) return
  mutating.value = true
  mutationError.value = null
  const target = removingConfirm.value
  try {
    await adminClient().removeMember(create(RemoveMemberRequestSchema, { userId: target.userId }))
    members.value = members.value.filter((m) => m.userId !== target.userId)
    removingConfirm.value = null
  } catch (e) {
    mutationError.value = e instanceof ConnectError ? e.message : String(e)
  } finally {
    mutating.value = false
  }
}
</script>

<template>
  <div class="space-y-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="font-display text-3xl font-bold tracking-tight text-on-surface">
          Users &amp; Roles
        </h1>
        <p class="mt-2 max-w-2xl text-sm text-on-surface-variant">
          Invite teammates, change roles, and remove access. Limen routes every action through your
          organization's Zitadel directory — nothing is stored on Limen.
        </p>
      </div>
      <button
        type="button"
        class="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow hover:bg-primary/90"
        data-testid="members-invite-button"
        @click="openInvite"
      >
        <UserPlus class="size-4" />
        Invite User
      </button>
    </header>

    <!-- Filters -->
    <div class="flex flex-wrap items-center gap-3">
      <label class="relative min-w-[16rem] flex-1">
        <Search
          class="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-on-surface-variant"
        />
        <input
          v-model="searchQuery"
          type="search"
          placeholder="Search by name or email…"
          data-testid="members-search"
          class="w-full rounded-md border border-outline bg-surface py-2 pl-9 pr-3 text-sm text-on-surface placeholder:text-on-surface-variant focus:border-primary focus:outline-none"
          @input="onSearchInput"
        />
      </label>
      <select
        v-model="roleFilter"
        data-testid="members-role-filter"
        class="rounded-md border border-outline bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none"
      >
        <option :value="MemberRole.UNSPECIFIED">All roles</option>
        <option :value="MemberRole.MEMBER">Member</option>
        <option :value="MemberRole.ADMIN">Admin</option>
        <option :value="MemberRole.OWNER">Owner</option>
      </select>
    </div>

    <!-- Table -->
    <section
      class="overflow-hidden rounded-lg border border-outline-variant bg-surface"
      data-testid="members-table"
    >
      <div v-if="loading" class="flex items-center gap-2 p-6 text-sm text-on-surface-variant">
        <Loader2 class="size-4 animate-spin" /> Loading members…
      </div>
      <div v-else-if="error" class="p-4 text-sm text-error" data-testid="members-error">
        Failed to load members: {{ error }}
      </div>
      <div
        v-else-if="members.length === 0"
        class="p-6 text-center text-sm text-on-surface-variant"
        data-testid="members-empty"
      >
        No members match your filters.
      </div>
      <table v-else class="w-full text-left text-sm">
        <thead class="bg-surface-variant/40 text-xs uppercase text-on-surface-variant">
          <tr>
            <th class="px-4 py-3">User</th>
            <th class="px-4 py-3">Role</th>
            <th class="px-4 py-3">Status</th>
            <th class="px-4 py-3">Last login</th>
            <th class="px-4 py-3 text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="m in members"
            :key="m.userId"
            class="border-t border-outline-variant"
            :data-testid="`members-row-${m.userId}`"
          >
            <td class="px-4 py-3">
              <div class="flex items-center gap-3">
                <div
                  class="flex size-9 items-center justify-center rounded-full bg-primary/15 text-xs font-semibold text-primary"
                  aria-hidden="true"
                >
                  {{ initials(m) }}
                </div>
                <div class="min-w-0">
                  <div class="truncate font-medium text-on-surface">
                    {{ m.displayName || m.email }}
                  </div>
                  <div class="truncate text-xs text-on-surface-variant">{{ m.email }}</div>
                </div>
              </div>
            </td>
            <td class="px-4 py-3">
              <span class="inline-flex items-center gap-1.5 text-on-surface">
                <component :is="roleIcon(m.role)" class="size-3.5" />
                {{ ROLE_LABELS[m.role] }}
              </span>
            </td>
            <td class="px-4 py-3">
              <span
                class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium"
                :class="STATE_CLASS[m.state]"
              >
                {{ STATE_LABELS[m.state] }}
              </span>
            </td>
            <td class="px-4 py-3 text-on-surface-variant">{{ formatRelative(m.lastLogin) }}</td>
            <td class="px-4 py-3">
              <div class="flex justify-end gap-1">
                <button
                  type="button"
                  class="rounded p-1.5 text-on-surface-variant hover:bg-surface-variant hover:text-on-surface"
                  :title="`Change role for ${m.email}`"
                  :data-testid="`members-edit-${m.userId}`"
                  @click="openEdit(m)"
                >
                  <Pencil class="size-4" />
                </button>
                <button
                  type="button"
                  class="rounded p-1.5 text-on-surface-variant hover:bg-error/10 hover:text-error disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-on-surface-variant"
                  :title="
                    isLastOwner(m)
                      ? 'Cannot remove the last owner. Promote another member to owner first.'
                      : `Remove ${m.email}`
                  "
                  :disabled="isLastOwner(m)"
                  :data-testid="`members-remove-${m.userId}`"
                  @click="askRemove(m)"
                >
                  <Trash2 class="size-4" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- Identity & policies disclosure -->
    <details class="rounded-lg border border-outline-variant bg-surface">
      <summary
        class="flex cursor-pointer items-center justify-between gap-2 px-4 py-3 text-sm font-medium text-on-surface"
      >
        <span>Identity &amp; policies</span>
        <ChevronDown class="size-4" />
      </summary>
      <div class="border-t border-outline-variant p-4">
        <p class="mb-4 text-sm text-on-surface-variant">
          These settings live in Zitadel Console and are not duplicated in Limen.
        </p>
        <ZitadelDirectory :issuer="issuer" :org-id="orgId" :cards="directoryCards" />
      </div>
    </details>

    <!-- Invite modal -->
    <Teleport to="body">
      <div
        v-if="inviteOpen"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
        @click.self="closeInvite"
      >
        <div
          class="w-full max-w-md rounded-lg bg-surface p-5 shadow-xl"
          role="dialog"
          aria-modal="true"
          data-testid="members-invite-modal"
        >
          <div class="mb-4 flex items-center justify-between">
            <h2 class="font-display text-lg font-semibold text-on-surface">Invite a user</h2>
            <button
              type="button"
              class="rounded p-1 text-on-surface-variant hover:bg-surface-variant"
              @click="closeInvite"
            >
              <X class="size-4" />
            </button>
          </div>
          <form class="space-y-3" @submit.prevent="submitInvite">
            <label class="block text-sm">
              <span class="mb-1 block font-medium text-on-surface">Email</span>
              <div class="relative">
                <Mail
                  class="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-on-surface-variant"
                />
                <input
                  v-model="inviteForm.email"
                  type="email"
                  required
                  data-testid="members-invite-email"
                  class="w-full rounded-md border border-outline bg-surface py-2 pl-9 pr-3 text-on-surface placeholder:text-on-surface-variant focus:border-primary focus:outline-none"
                  placeholder="alice@example.com"
                />
              </div>
            </label>
            <div class="grid grid-cols-2 gap-3">
              <label class="block text-sm">
                <span class="mb-1 block font-medium text-on-surface">First name</span>
                <input
                  v-model="inviteForm.givenName"
                  type="text"
                  data-testid="members-invite-given-name"
                  class="w-full rounded-md border border-outline bg-surface px-3 py-2 text-on-surface focus:border-primary focus:outline-none"
                />
              </label>
              <label class="block text-sm">
                <span class="mb-1 block font-medium text-on-surface">Last name</span>
                <input
                  v-model="inviteForm.familyName"
                  type="text"
                  data-testid="members-invite-family-name"
                  class="w-full rounded-md border border-outline bg-surface px-3 py-2 text-on-surface focus:border-primary focus:outline-none"
                />
              </label>
            </div>
            <label class="block text-sm">
              <span class="mb-1 block font-medium text-on-surface">Role</span>
              <select
                v-model="inviteForm.role"
                data-testid="members-invite-role"
                class="w-full rounded-md border border-outline bg-surface px-3 py-2 text-on-surface focus:border-primary focus:outline-none"
              >
                <option :value="MemberRole.MEMBER">Member</option>
                <option :value="MemberRole.ADMIN">Admin</option>
                <option :value="MemberRole.OWNER">Owner</option>
              </select>
            </label>
            <p
              v-if="inviteError"
              class="rounded border border-error/40 bg-error/10 p-2 text-xs text-error"
              data-testid="members-invite-error"
            >
              {{ inviteError }}
            </p>
            <div class="mt-4 flex justify-end gap-2">
              <button
                type="button"
                class="rounded-md px-3 py-2 text-sm text-on-surface-variant hover:bg-surface-variant"
                @click="closeInvite"
              >
                Cancel
              </button>
              <button
                type="submit"
                :disabled="mutating"
                data-testid="members-invite-submit"
                class="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow hover:bg-primary/90 disabled:opacity-50"
              >
                <Loader2 v-if="mutating" class="size-4 animate-spin" />
                Send invite
              </button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Edit role modal -->
    <Teleport to="body">
      <div
        v-if="editing"
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
        @click.self="closeEdit"
      >
        <div
          class="w-full max-w-sm rounded-lg bg-surface p-5 shadow-xl"
          role="dialog"
          aria-modal="true"
          data-testid="members-edit-modal"
        >
          <h2 class="mb-3 font-display text-lg font-semibold text-on-surface">Change role</h2>
          <p class="mb-3 text-sm text-on-surface-variant">
            {{ editing.displayName || editing.email }}
          </p>
          <select
            v-model="editingRole"
            data-testid="members-edit-role"
            class="w-full rounded-md border border-outline bg-surface px-3 py-2 text-sm text-on-surface focus:border-primary focus:outline-none"
          >
            <option :value="MemberRole.MEMBER">Member</option>
            <option :value="MemberRole.ADMIN">Admin</option>
            <option :value="MemberRole.OWNER">Owner</option>
          </select>
          <p
            v-if="editingDemotesLastOwner"
            class="mt-3 rounded border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-300"
            data-testid="members-edit-last-owner-warning"
          >
            This is the last owner of the organization. Promote another member to owner before
            changing this user's role.
          </p>
          <p
            v-if="mutationError"
            class="mt-3 rounded border border-error/40 bg-error/10 p-2 text-xs text-error"
          >
            {{ mutationError }}
          </p>
          <div class="mt-4 flex justify-end gap-2">
            <button
              type="button"
              class="rounded-md px-3 py-2 text-sm text-on-surface-variant hover:bg-surface-variant"
              @click="closeEdit"
            >
              Cancel
            </button>
            <button
              type="button"
              :disabled="mutating || editingDemotesLastOwner"
              data-testid="members-edit-submit"
              class="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-2 text-sm font-medium text-on-primary shadow hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
              @click="submitEdit"
            >
              <Loader2 v-if="mutating" class="size-4 animate-spin" />
              Save
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Remove confirm modal -->
    <ErrorModal
      :open="!!removingConfirm"
      title="Remove member"
      :message="
        removingConfirm
          ? `Remove ${removingConfirm.displayName || removingConfirm.email} from this organization? This deletes the user in Zitadel and cannot be undone.`
          : ''
      "
      primary-label="Remove"
      secondary-label="Cancel"
      @primary="confirmRemove"
      @secondary="closeRemove"
      @close="closeRemove"
    />
  </div>
</template>
