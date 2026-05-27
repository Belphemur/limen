import type { Component } from 'vue'
import {
  LayoutDashboard,
  Brain,
  Building2,
  Code2,
  Server,
  SlidersHorizontal,
  Users,
  KeyRound,
} from '@lucide/vue'

// Single source of truth for every admin SPA route.
//
// - `ROUTES` is the canonical path map. Use it instead of string
//   literals anywhere you need to navigate (`router.push(ROUTES.x)`,
//   `<RouterLink :to="ROUTES.x">`).
// - `routeDefs` is consumed by `router/index.ts` to build the
//   vue-router config — component lazy-imports + meta live here so
//   route metadata (e.g. contextual-search mode) is co-located with
//   the path.
// - `navTree` is consumed by `layout/Sidebar.vue` to render the
//   sidebar. Adding a new sidebar entry means adding it here and
//   nowhere else.

export const ROUTES = {
  dashboard: '/',
  mcpServers: '/mcp-servers',
  mcpServerNew: '/mcp-servers/new',
  mcpServerDetail: '/mcp-servers/:id',
  members: '/org/members',
  settings: '/org/settings',
  ideConfiguration: '/org/ide-configuration',
  serviceAccounts: '/org/service-accounts',
  serviceAccountDetail: '/org/service-accounts/:id',
  forbidden: '/forbidden',
  oauthPopupClose: '/oauth-popup-close',
  signup: '/signup',
  signupVerify: '/signup/verify',
} as const

export type SearchMode = 'filter' | 'palette' | 'hidden'
export interface SearchMeta {
  mode: SearchMode
  placeholder?: string
}

export interface RouteDef {
  name: string
  path: string
  component: () => Promise<Component | { default: Component }>
  search: SearchMeta
  // Routes flagged `public` skip the session-bootstrap router guard.
  // Used for /forbidden so that a permission_denied bootstrap can
  // still render the page it's redirecting to.
  public?: boolean
  // Routes flagged `outsideShell` render at the top level instead of
  // inside AdminShell — used for full-bleed error pages.
  outsideShell?: boolean
}

export const routeDefs: RouteDef[] = [
  {
    name: 'dashboard',
    path: ROUTES.dashboard,
    component: () => import('@/pages/Dashboard.vue'),
    search: { mode: 'palette', placeholder: 'Jump to upstream, member, or setting…' },
  },
  {
    name: 'mcp-servers',
    path: ROUTES.mcpServers,
    component: () => import('@/pages/McpServers.vue'),
    search: { mode: 'filter', placeholder: 'Filter MCP servers…' },
  },
  {
    name: 'mcp-server-new',
    path: ROUTES.mcpServerNew,
    component: () => import('@/pages/McpServerNew.vue'),
    search: { mode: 'hidden' },
  },
  {
    name: 'mcp-server-detail',
    path: ROUTES.mcpServerDetail,
    component: () => import('@/pages/McpServerDetail.vue'),
    search: { mode: 'filter', placeholder: 'Filter tools…' },
  },
  {
    name: 'members',
    path: ROUTES.members,
    component: () => import('@/pages/Members.vue'),
    search: { mode: 'filter', placeholder: 'Filter members…' },
  },
  {
    name: 'settings',
    path: ROUTES.settings,
    component: () => import('@/pages/Settings.vue'),
    search: { mode: 'hidden' },
  },
  {
    name: 'ide-configuration',
    path: ROUTES.ideConfiguration,
    component: () => import('@/pages/IDEConfiguration.vue'),
    search: { mode: 'hidden' },
  },
  {
    name: 'service-accounts',
    path: ROUTES.serviceAccounts,
    component: () => import('@/pages/ServiceAccounts.vue'),
    search: { mode: 'filter', placeholder: 'Filter service accounts…' },
  },
  {
    name: 'service-account-detail',
    path: ROUTES.serviceAccountDetail,
    component: () => import('@/pages/ServiceAccountDetail.vue'),
    search: { mode: 'hidden' },
  },
  {
    name: 'forbidden',
    path: ROUTES.forbidden,
    component: () => import('@/pages/Forbidden.vue'),
    search: { mode: 'hidden' },
    public: true,
    outsideShell: true,
  },
  {
    name: 'oauth-popup-close',
    path: ROUTES.oauthPopupClose,
    component: () => import('@/pages/OAuthPopupClose.vue'),
    search: { mode: 'hidden' },
    public: true,
    outsideShell: true,
  },
  {
    name: 'signup',
    path: ROUTES.signup,
    component: () => import('@/pages/SignupStart.vue'),
    search: { mode: 'hidden' },
    public: true,
    outsideShell: true,
  },
  {
    name: 'signup-verify',
    path: ROUTES.signupVerify,
    component: () => import('@/pages/SignupVerify.vue'),
    search: { mode: 'hidden' },
    public: true,
    outsideShell: true,
  },
]

export interface NavLeaf {
  kind: 'leaf'
  label: string
  path: string
  icon: Component
}
export interface NavGroup {
  kind: 'group'
  label: string
  icon: Component
  children: NavLeaf[]
}
export type NavNode = NavLeaf | NavGroup

export const navTree: NavNode[] = [
  { kind: 'leaf', label: 'Dashboard', path: ROUTES.dashboard, icon: LayoutDashboard },
  {
    kind: 'group',
    label: 'LLM',
    icon: Brain,
    children: [{ kind: 'leaf', label: 'MCP Servers', path: ROUTES.mcpServers, icon: Server }],
  },
  {
    kind: 'group',
    label: 'Organization',
    icon: Building2,
    children: [
      { kind: 'leaf', label: 'Org Settings', path: ROUTES.settings, icon: SlidersHorizontal },
      { kind: 'leaf', label: 'IDE Configuration', path: ROUTES.ideConfiguration, icon: Code2 },
      { kind: 'leaf', label: 'Users & Roles', path: ROUTES.members, icon: Users },
      { kind: 'leaf', label: 'Service Accounts', path: ROUTES.serviceAccounts, icon: KeyRound },
    ],
  },
]
