# Phase 17 — Policy engine (tag-based IAM for users, upstreams, tools, clients)

**Depends on**: [Phase 4](phase-04-tenant-auth-session.md) (users + roles claim),
[Phase 6](phase-06-resource-server.md) (MCP RS request path),
[Phase 7](phase-07-outbound-upstream.md) + [Phase 8](phase-08-per-tenant-injection.md)
(upstream registry + per-user link state),
[Phase 7b](phase-07b-dcr-per-client-project.md) (DCR’d MCP clients—the client principal
we tag in v1),
[Phase 9c](phase-09c-tenant-admin-spa.md) (tenant admin SPA + AdminService),
[`docs/audit.md`](../audit.md) (audit writer).
**Unblocks**: tenants can restrict which users — in which client — can
reach which upstreams or call which tools, without forking Zitadel
roles for every product decision; staff support can answer "why was
Bob denied `jira_create_issue`?" in one query.

## Goal

Today the only authorization surface a tenant admin can drive is the
Zitadel project role (`owner` / `admin` / `member`) plus per-user
upstream-link enable/disable ([Phase 9c](phase-09c-tenant-admin-spa.md)).
That's not enough for any real customer:

- "Contractors must not reach our production GitHub."
- "Only the security team can call `code_scanning_*` tools."
- "The QA org can use Linear read-only; no `linear_create_issue`."

We want a **tag + policy** model — small enough that a non-engineer
admin can reason about it, expressive enough to capture the cases above
without us shipping a new code path per request.

The shape borrows from AWS IAM tags and Kubernetes labels but stays
deliberately simpler: no resource ARNs, no condition DSL, no inline
JSON policy editor in v1. **Tags are `key=value` pairs** (both lowercase
strings); policies are rows; evaluation is a pure function. Single-name
tags from a v0 sketch are gone — every tag has a key and a value because
every real customer ask we collected (`role=contractor`, `env=prod`,
`data=read-only`, `pii=true`) already wanted that shape, and modelling
it upfront kills the "now I have eight tags called `prod-something`"
failure mode.

## Non-goals (v1)

- **Time-based / conditional rules** (deny outside business hours,
  rate-limit-per-policy, IP allowlists). Designed-for via the
  `condition_json` column but the evaluator ignores it in v1.
- **Surface as a selector dimension** (`mcp_direct` vs `codemode`).
  Conceptually clean but no customer ask yet; the codemode script
  body is already audited per [`docs/audit.md`](../audit.md), which
  covers the obvious oversight need.
- **Cross-tenant policies**. Policies are tenant-scoped (RLS). Staff
  may **read** them via the cross-tenant SELECT path
  ([Phase 12](phase-12-staff-backoffice.md)) but cannot author them.
  Cross-tenant **tagging of tenants themselves** (e.g. `tier=enterprise`,
  `region=eu`) is a staff-side feature tracked in
  [Phase 12](phase-12-staff-backoffice.md), not here.
- **Policy on individual tool arguments** ("deny `jira_create_issue`
  when `project=PROD`"). Argument-shape policies are a future phase;
  v1 gates on `(client, upstream, tool_name)` triples only.
- **Custom Zitadel roles**. The three project roles stay fixed — the
  policy engine sits orthogonal to them. A `member` with policy
  `allow:*` is effectively an admin for tool access; the role still
  governs portal write actions.

## Design

### Entity model

Three new tables (all tenant-scoped, RLS-forced):

```sql
CREATE TABLE tags (
  id            BIGSERIAL PRIMARY KEY,
  public_id     TEXT NOT NULL UNIQUE,                  -- tag_<ulid>
  tenant_id     BIGINT NOT NULL REFERENCES tenants(id),
  key           TEXT NOT NULL,                          -- lowercase, [a-z0-9_-]+
  value         TEXT NOT NULL,                          -- lowercase, [a-z0-9_.-]+; '*' is reserved
  description   TEXT NOT NULL DEFAULT '',
  color         TEXT,                                   -- hex, optional UI hint; defaults to a hash of key
  is_system     BOOLEAN NOT NULL DEFAULT false,         -- true for the starter set (see below); rename/delete protected
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX tags_tenant_key_value_uq
  ON tags (tenant_id, key, value) WHERE deleted_at IS NULL;

CREATE TYPE policy_subject_kind AS ENUM ('user', 'upstream', 'tool', 'client');

CREATE TABLE tag_bindings (
  id            BIGSERIAL PRIMARY KEY,
  tenant_id     BIGINT NOT NULL REFERENCES tenants(id),
  tag_id        BIGINT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  subject_kind  policy_subject_kind NOT NULL,
  subject_id    BIGINT NOT NULL,                        -- users.id | upstreams.id | upstream_tools.id | mcp_clients.id
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX tag_bindings_uq
  ON tag_bindings (tag_id, subject_kind, subject_id)
  WHERE deleted_at IS NULL;

CREATE TYPE policy_effect AS ENUM ('allow', 'deny');

CREATE TABLE policies (
  id              BIGSERIAL PRIMARY KEY,
  public_id       TEXT NOT NULL UNIQUE,                 -- pol_<ulid>
  tenant_id       BIGINT NOT NULL REFERENCES tenants(id),
  name            TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  effect          policy_effect NOT NULL,
  priority        INTEGER NOT NULL DEFAULT 100,         -- lower wins; ties broken by deny > allow
  enabled         BOOLEAN NOT NULL DEFAULT true,

  -- Selectors. All ANY-of within a column, AND across columns.
  -- Empty array means "any" for that dimension.
  -- Tag selectors are tag_id arrays; resolution is exact (key=value)
  -- plus an optional key-only wildcard form (see "Tag selectors" below).
  subject_tags_json    JSONB NOT NULL DEFAULT '[]',     -- user tags
  upstream_tags_json   JSONB NOT NULL DEFAULT '[]',     -- upstream tags
  tool_tags_json       JSONB NOT NULL DEFAULT '[]',     -- tool tags  (e.g. data=read-only, risk=destructive)
  client_tags_json     JSONB NOT NULL DEFAULT '[]',     -- mcp-client tags (e.g. kind=ide, kind=automation)
  upstream_ids_json    JSONB NOT NULL DEFAULT '[]',     -- explicit upstream ids
  client_ids_json      JSONB NOT NULL DEFAULT '[]',     -- explicit mcp-client ids
  tool_patterns_json   JSONB NOT NULL DEFAULT '[]',     -- ['jira_*', 'confluence_get_page']

  condition_json  JSONB NOT NULL DEFAULT '{}',          -- reserved; ignored by v1 evaluator

  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at      TIMESTAMPTZ
);
CREATE UNIQUE INDEX policies_tenant_name_uq
  ON policies (tenant_id, name) WHERE deleted_at IS NULL;
```

#### Tool + client taggability

Both `upstream_tools` (the per-upstream catalogue persisted by
[Phase 8](phase-08-per-tenant-injection.md) / [Phase 14](phase-14-upstream-tool-normalization.md))
and `mcp_clients` (the DCR’d OAuth clients from
[Phase 7b](phase-07b-dcr-per-client-project.md)) are tenant-scoped
rows with a `BIGINT id`, which is all `tag_bindings.subject_id` needs.
If either table does not yet exist when this phase ships, the
migration adds the minimum `(id, public_id, tenant_id, ...)` shape
required for tag binding — actual catalogue lifecycle stays with its
owning phase.

Tool tags can come from two places:

- **Admin-curated** in the tag UI (the default; what's documented here).
- **Upstream-declared**, via an optional `_meta.limen.tags` extension
  on the tool definition returned by `tools/list` upstream-side. The
  importer reads them as suggestions, prefixes them with
  `auto:` (so `auto:risk=destructive`), and surfaces them in the
  admin UI for one-click acceptance. We never enforce solely on an
  unverified upstream-declared tag — admins promote them.

MCP-client tags are admin-curated only; clients can't tag themselves.

#### Tag selectors

A tag selector entry is normally an exact `tag_id` (i.e. an exact
`key=value` pair). To express "any value for this key" — e.g. "deny
for any `env=*` policy with `env=prod` _and_ `env=staging` covered by
the same row — a selector entry may be the form `{"key": "env"}`
instead of a numeric id. The evaluator resolves these at load time
into the set of `tag_ids` matching the key for the tenant. This is the
only piece of structure in the JSONB blob; everything else is opaque
integers.

Notes:

- Selectors are JSONB arrays, not join tables, because the read path
  needs the full policy in one row — and the cardinality is small
  (dozens of policies per tenant, single-digit selector lengths). We
  index `policies (tenant_id, enabled, priority)` and the evaluator
  loads the tenant's full enabled policy set once per request and
  filters in Go. No `IN` blowup.
- `tag_bindings` is the only join table. It is single-column-typed by
  `subject_kind` rather than split into four tables so the
  tag-management UI and admin RPCs work against one schema. The
  `subject_id` integrity is enforced by the application; we
  deliberately do **not** FK to four tables (no Postgres feature for
  that without triggers, and DRY > validation redundancy here).
- All tables RLS-forced under the [Phase 3](phase-03-postgres-rls.md)
  pattern. Standard `tenant_id = current_setting('limen.tenant_id')`
  policy. Staff cross-tenant `SELECT` honours `limen.staff_mode`
  ([Phase 12](phase-12-staff-backoffice.md)).

#### Starter tag set

When a tenant is provisioned (or migrated onto this phase) we seed a
small, opinionated `is_system=true` tag set. They cover the patterns
that showed up in every customer ask we collected; an admin who never
touches the Tags page still has something useful to apply on day one.

| Key    | Values                                                                  | Bound to          | Used in starter policy?                          |
| ------ | ----------------------------------------------------------------------- | ----------------- | ------------------------------------------------ |
| `role` | `contractor`, `fulltime`, `service`                                     | users / svc accts | yes (deny contractor → env=prod)                 |
| `team` | `platform`, `backend`, `frontend`, `data`, `sre`, `security`, `support` | users / svc accts | no — example for admins to copy & extend         |
| `env`  | `prod`, `staging`, `dev`                                                | upstreams         | yes (deny contractor → env=prod)                 |
| `data` | `read-only`, `read-write`                                               | tools             | yes (member-default → allow data=read-only only) |
| `risk` | `destructive`, `safe`                                                   | tools             | no — example for admins to copy                  |
| `pii`  | `true`, `false`                                                         | tools / upstreams | no — example                                     |
| `kind` | `ide`, `agent`, `automation`                                            | mcp clients       | no — example                                     |

The `team` values are deliberately a generic engineering-org slice — admins
rename / add / remove values freely (the key is `is_system`, the values
under it are not). Common policy patterns it unlocks: "only `team=data` may
reach the Snowflake upstream", "`team=security` gets `risk=destructive`
tools that everyone else is denied", "service account
`role=service, team=ci` may call CI upstreams only".

Alongside, we seed two `is_system=true` example policies, **disabled
by default** so they document the shape without changing tenant
behaviour on upgrade:

1. **`example-contractor-deny-prod`** — `deny`, priority 50,
   `subject_tags=[role=contractor]`, `upstream_tags=[env=prod]`,
   `tool_patterns=[]`. Reads as "contractors get nothing on prod".
2. **`example-member-readonly`** — `allow`, priority 200,
   `subject_tags=[]` (anyone), `tool_tags=[data=read-only]`. With the
   tenant default flipped to `deny`, this becomes the "everyone can
   read, nobody can write" baseline.

System tags are protected against rename/delete in the admin UI (the
buttons are disabled with an explanatory tooltip); admins can still
_unbind_ them or _disable_ the system policies freely. New values for
a system key are allowed — a tenant can add `env=qa` without losing
the protection on `env=prod`.

Seeding lives in `internal/policy/seed.go` and runs once per tenant
on creation (and as a one-shot backfill for the migration). The
seeder is idempotent on `(tenant_id, key, value)`.

### Evaluation contract

A pure function in `internal/policy/eval.go`:

```go
type Decision struct {
    Effect    Effect     // Allow | Deny
    PolicyID  int64      // 0 if default
    Reason    string     // short, surfaced to logs + (optionally) the model
}

type Request struct {
    UserID         int64
    UserTagIDs     []int64           // resolved once at request entry
    ClientID       int64             // the DCR'd MCP client; 0 for portal-internal calls
    ClientTagIDs   []int64
    UpstreamID     int64
    UpstreamTagIDs []int64
    ToolID         int64             // 0 when evaluating whole-upstream visibility
    ToolTagIDs     []int64
    ToolName       string             // e.g. "jira_search"
}

func Evaluate(req Request, policies []Policy) Decision
```

Algorithm (deterministic, no I/O):

1. Filter `policies` to those whose every populated selector matches
   the request:
   - `subject_tags`: `req.UserTagIDs` ∩ `policy.subject_tags` ≠ ∅
     (empty selector = match-all).
   - `upstream_tags`: same.
   - `tool_tags`: `req.ToolTagIDs` ∩ `policy.tool_tags` ≠ ∅
     (empty = match-all).
   - `client_tags`: same against `req.ClientTagIDs`.
   - `upstream_ids`: `req.UpstreamID` ∈ list (empty = match-all).
   - `client_ids`: `req.ClientID` ∈ list (empty = match-all).
   - `tool_patterns`: glob-match against `req.ToolName` (empty = match-all).
2. Sort matching policies by `(priority ASC, effect DESC)` —
   so lower `priority` wins; ties go to `deny` first.
3. Take the first row. If none, return the **default decision** (see
   below).

#### Default decision

Per tenant setting (`tenant_settings.policy_default`, enum
`allow` | `deny`). Ships as `allow` for backwards compat with the v0
"no policies" world — flipping a tenant to default-deny is an explicit
admin action and is the recommended posture for security-conscious
customers. The setting is surfaced in two places:

1. The **Policies** admin page header: a single toggle labelled
   **"Default action when no policy matches: Allow / Deny"**.
2. The admin **Dashboard** onboarding bento (see _Onboarding
   integration_ below): a dedicated setup card the admin must
   acknowledge before the new-tenant checklist completes. Until the
   admin opens that card and confirms a choice, the value remains at
   the seeded default (`allow`) but the dashboard card stays
   un-ticked — so a tenant that never visits the policies page still
   sees a clear nudge.

### Where it runs

The evaluator is called at every level of the visibility / dispatch
stack so a denied resource is **uniformly invisible** — never half-
hidden, never visible-but-unusable. Three gates, one `Evaluate`:

1. **Upstream-level visibility.** Before any tool listing happens we
   ask: "with `tool_name = *`, is this whole upstream usable by this
   user?" — i.e. does an `Evaluate` against the wildcard tool return
   `Deny` from a policy whose `tool_patterns` is empty (match-all)?
   If yes, the upstream is treated as if it did not exist for this
   user:
   - **Portal**: hidden from the user-facing Upstreams page, hidden
     from the connect/link prompts, hidden from MCP-client config
     downloads. Admins still see it on the admin Upstreams page (so
     they can manage the policy), but with a "Hidden from N users by
     policy `<name>`" badge.
   - **MCP `tools/list`**: every tool from that upstream is dropped.
   - **Codemode `codemode.tools()`**: the entire upstream group is
     dropped — the user-visible JS sees no entry for it in the
     listing array, and no alias autodiscovery
     ([Phase 8c](phase-08c-ambient-context-and-alias-discovery.md))
     resolves against it.
     This is implemented as a single helper
     `internal/policy/eval.go::UpstreamDenied(req, policies)` that
     short-circuits when at least one matching `deny` policy carries
     empty `tool_patterns` **and** empty `tool_tags` — i.e. it forbids
     everything on that upstream. Per-tool deny policies (those that
     narrow by pattern or tool tag) do **not** hide the upstream; only
     blanket denies do.
2. **`tools/list` per-tool filtering** ([Phase 6](phase-06-resource-server.md)
   - [Phase 8](phase-08-per-tenant-injection.md)): after the per-user
     link filter and the upstream-visibility filter above, drop any
     remaining `(upstream, tool)` pair where `Evaluate(...).Effect ==
Deny`. The model never sees tools it cannot call.
3. **`tools/call` dispatch** ([Phase 8](phase-08-per-tenant-injection.md)
   - [Phase 8b](phase-08b-codemode-async-tool-calls.md) for codemode):
     evaluate again at call time and return a structured MCP error if
     denied. We do not trust the listing filters alone — a misbehaving
     client or a stale cache could call a tool we never advertised. A
     call into a fully-denied upstream returns the same `policy_denied`
     error as a per-tool deny; the response does not leak that the
     upstream exists.

The policy set is loaded once per inbound MCP request **and** once per
portal RPC that lists upstreams, and cached on the request context
(`internal/policy/loader.go::LoadForRequest`). Loading runs through
the standard `storage.Session(ctx)` path so RLS applies. Tag
membership for the requesting user and for every upstream the session
might touch is precomputed at the same time — one query per table,
single round-trip to Postgres.

#### Admin-versus-user view

The "hidden upstream" rule applies to **every principal that would
call into the gateway** — owner, admin, member, service account
([Phase 16](phase-16-observability-and-active-users.md)). It does
**not** apply to administrative views whose job is to manage the
policies themselves:

- `AdminService.ListUpstreams` returns every upstream regardless of
  the caller's policy footprint, decorated with a `denied_for_self:
bool` flag so the SPA can render the "you cannot use this — only
  manage it" affordance.
- `PortalService.ListMyUpstreams` (the user-facing listing) applies
  the gate.
- The MCP RS hot path always applies the gate; there is no
  "administrative MCP" surface.

This split exists so an admin who has written a policy denying
themselves a sensitive upstream can still see + edit the policy.
They cannot, however, call tools on that upstream without first
relaxing the policy — denial is symmetric across roles.

### Codemode interaction

The codemode JS sandbox calls upstream tools through the same dispatch
path, so deny decisions surface as the rejected `Promise` from
[`codemode.tool.*`](phase-08b-codemode-async-tool-calls.md) calls. The
rejection's `error.code` is `"policy_denied"`; `error.message` carries
the policy name (e.g. `"contractor-deny-prod"`) and `error.reason`
carries a short human string ("Your role 'contractor' is denied
`github_*` by policy 'contractor-deny-prod'"). This is intentionally
informative — the model can adapt, the user can see why, and the
policy author can ship descriptive names. The `policy_id` (numeric)
is **not** exposed to the sandbox; only the public name.

The empty-filter hints from [Phase 8c](phase-08c-ambient-context-and-alias-discovery.md)
gain a new failure mode: `codemode.tools()` may legitimately return
zero matching groups because policy hid them all (per-tool or
whole-upstream). The hint envelope gains an optional
`hidden_by_policy_count: int` field so the model can distinguish "no
such upstream" from "exists but you can't see it". The count is
deliberately coarse — we do not name the hidden upstreams, since
disclosing their existence would leak the policy shape.

### Admin UX (tenant admin SPA)

Three new sub-routes under `/t/<tenant>/portal/admin/`:

- **Tags** — list grouped by key, with values as chips under each key.
  Create / edit / delete operate at the **value** level; creating a new
  key is implicit (typing `env` in the key autocomplete when only
  `role` exists creates the `env` key on first save). Each value row
  shows binding counts split by subject kind (users / upstreams /
  tools / clients) and a count of policies referencing it. System tags
  (`is_system=true`) render with a small lock icon; rename + delete
  are disabled with a tooltip pointing to the disable-policy /
  unbind-tag alternatives. Deleting a value that is referenced by
  ≥ 1 policy prompts a confirmation modal listing them.
- **Policies** — list with columns `Name | Effect | Subject | Resource
| Tool | Client | Priority | Status`. Default sort by priority. New /
  Edit form is a single page with four picker rows ("Who" subject tags,
  "Through which client" client tags + explicit clients, "Where"
  upstream tags + explicit upstreams, "What" tool tags + tool name
  patterns) plus the effect toggle and priority slider. Tag pickers
  group options by key with a typeahead that searches across both
  key and value (`env:p` matches `env=prod`). Form is validated
  against `Evaluate` in a preview pane: the admin picks a sample
  user **and** sample client and the UI shows which tools that pair
  would see / be denied.
- **Test policy** — full-page evaluator: pick `user × client ×
upstream × tool`, see the winning policy or "default action" and a
  trace of which policies matched and were beaten. This is the same
  kernel the staff backoffice exposes (below).

The `Tools` admin page ([Phase 14](phase-14-upstream-tool-normalization.md)
lands the catalogue UI; this phase only adds the column) gains a tag
picker column. The `MCP Clients` admin page
([Phase 7b](phase-07b-dcr-per-client-project.md)) gains the same
tag picker column.

Tag pickers everywhere render the tag colour pill (one colour per
key, value renders as text after `:` — e.g. a blue `env: prod` pill
with a darker blue `env: staging` next to it) — copying the
Material-3 surface tone idiom already used in the dashboard.

Owner + admin roles can edit; member is read-only on the Tags page and
sees Policies hidden from the nav.

#### Onboarding integration

The admin **Dashboard** ([web/admin/src/pages/Dashboard.vue](../../web/admin/src/pages/Dashboard.vue))
already ships a four-card setup bento (`connect`, `ide`, `invite`,
`configure`). Phase 17 adds a **fifth** card so picking a policy
posture is part of new-tenant onboarding rather than a setting an
admin discovers months later when a deny doesn't fire as expected.

UX:

- **Card key:** `policy` (slots between `ide` and `invite` in the
  bento order, so the row goes `connect → ide → policy → invite →
configure`). On screens narrower than `xl` the grid wraps as today.
- **Icon:** `ShieldCheck` from `@lucide/vue` (matches the existing
  iconography style for security surfaces).
- **Title:** "Choose Your Default Policy".
- **Body:** "Decide what happens when no policy matches a request.
  **Allow** is permissive and recommended for trial tenants; **Deny**
  is fail-closed and recommended for production."
- **CTA:** "Pick a default" → routes to `ROUTES.policiesDefault`
  (lands the admin on the Policies page with the toggle scrolled
  into view and a one-paragraph explainer above it). A **"Skip for
  now"** link under the card (mirroring the IDE card's skip pattern)
  marks the step done without changing the value — the default stays
  `allow` and the audit row records `payload_json.skipped=true`.
- **Done state:** the card ticks once `tenant_settings.policy_default_chosen_at`
  is non-empty. That timestamp is set by either of two paths:
  1. `AdminService.SetPolicyDefault` (the toggle on the Policies page) —
     sets `policy_default` to the chosen value AND stamps
     `policy_default_chosen_at`. Subsequent changes only update the
     value, never the timestamp.
  2. `AdminService.MarkPolicyDefaultSkipped` (the dashboard "Skip"
     link) — leaves `policy_default` untouched but stamps the
     timestamp.
- **Step count:** `SetupProgress` total goes from 4 → 5; the
  `steps` computed in `Dashboard.vue` gains a `{ key: 'policy', done:
settings.value.chosePolicyDefault }` entry.
- **Settings shape:** the local `DashboardSettings` interface in
  `Dashboard.vue` gains `chosePolicyDefault: boolean`, populated from
  `r.settings?.policyDefaultChosenAt`.

No new top-level route is needed — the existing Policies route handles
both the toggle and the deep-link anchor.

### Worked examples & UX walkthroughs

Each example below is written as the admin would experience it in the SPA
and as it lands in the database. Policy rows use this shorthand:

```
Policy: <slug>
  Effect: <allow|deny>   Priority: <int>   Enabled: <yes|no>
  Subject tags:   <key=value …>     (matches USER tags, AND across tags)
  Client tags:    <key=value …>
  Upstream tags:  <key=value …>
  Tool tags:      <key=value …>
  Upstream IDs:   <ups_… …>         (explicit pinning, OR)
  Client IDs:     <cli_… …>
  Tool patterns:  <glob …>
```

Empty rows are wildcards. Selectors within a row AND together; multiple
values for the same key OR (the SPA renders this as a chip group). Policies
themselves combine by **highest priority wins**, then **deny beats allow**
at equal priority, then the tenant `policy_default` decides.

#### 1. The "safe by default" tenant (recommended starting point)

**Goal:** new tenants should fail-closed. Nothing reaches an upstream
until the admin says so.

1. Admin opens **Settings → Policies** and flips `policy_default` to
   `deny`. The header gains a red "Default deny" pill.
2. Admin opens **Tags**, sees the seeded starter set, picks `env=prod`
   from the starter list and applies it to the Linear upstream on the
   **Upstreams** detail page (Tags chip group → "Add tag" → `env:prod`).
3. Admin creates the first allow policy:

   ```
   Policy: allow-team-prod-linear
     Effect: allow   Priority: 100   Enabled: yes
     Subject tags:  role=member
     Upstream tags: env=prod
     Tool patterns: linear.*
   ```

4. The right-hand **Preview** pane lists "12 users × 1 upstream × 47
   tools → allowed". A user with no `role` tag is shown in the "would
   still be blocked" callout below.

#### 2. Contractor isolation (canonical multi-tag case)

**Goal:** contractors can use the staging GitHub upstream but never the
production one; they should not even see prod in the portal or in
codemode `tools()`.

Tags:

- Users: `Alice`, `Bob` already have `role=member`; admin adds
  `role=contractor` to `Carol`. Tag chips on the user row show both
  keys side-by-side.
- Upstreams: `github-prod` → `env=prod, kind=scm`. `github-staging` →
  `env=staging, kind=scm`.

Policy:

```
Policy: contractor-deny-prod
  Effect: deny    Priority: 50    Enabled: yes
  Subject tags:  role=contractor
  Upstream tags: env=prod
  (tool patterns + tool tags empty → whole-upstream deny)
```

Result, exactly as Carol sees it:

- **Portal → Upstreams**: `github-prod` row is **not rendered**.
  `github-staging` is. The chip "Hidden by policy: 1" appears at the
  bottom of the list with a link to a read-only explanation page.
- **Codemode**: `tools()` returns no `github_prod_*` entries; alias
  autodiscovery does not surface them either.
- **MCP config download**: `github-prod` is omitted from the generated
  `mcp.json`; the file ships only `github-staging`.

Alice (member) is unaffected — the deny policy never matches her.

The admin viewing the same upstream sees the row but with a
`denied_for_self=false, denied_user_count=1` badge so they know who is
locked out.

#### 3. Read-only role (tool-tag gate)

**Goal:** support staff can read tickets but never mutate them.

Tags:

- Users: support team gets `role=support`.
- Tools: admin opens **Tools → Filter by tag → `risk=write`** and bulk-
  applies `risk=write` to `create_issue`, `update_issue`,
  `delete_comment`, … across every upstream. Many of these come pre-
  tagged via `_meta.limen.tags` (shown as `auto:risk=write` chips on
  the tools page until promoted with one click).

Policy:

```
Policy: support-readonly
  Effect: deny   Priority: 75   Enabled: yes
  Subject tags: role=support
  Tool tags:    risk=write
```

`tools/list` returns 0 mutating tools for the support team across every
upstream they have access to; `tools/call` rejects any direct call to
those tool names with `policy_denied`. No upstream is hidden — they're
just read-only.

#### 4. PII data-class restriction

**Goal:** only the `data-stewards` group may call tools that touch PII.

Tags: users get `role=data-steward` (or not). Tools tagged with
`pii=true` (often `auto:pii=true` from upstream `_meta`).

Two policies, leaning on deny-wins at equal priority:

```
Policy: pii-default-deny
  Effect: deny   Priority: 100   Enabled: yes
  Tool tags:  pii=true

Policy: pii-stewards-allow
  Effect: allow  Priority: 100   Enabled: yes
  Subject tags: role=data-steward
  Tool tags:    pii=true
```

At priority 100 the deny would win on its own; the allow only fires for
matching users, where deny-wins still applies — so this **doesn't
work** as written. The Admin UX surfaces this exact mistake: the test-
policy panel, given `(user=alice, role=data-steward, tool=lookup_user)`,
shows "deny by `pii-default-deny`, but `pii-stewards-allow` also
matched — increase its priority above the deny to override."

Corrected:

```
Policy: pii-stewards-allow
  Effect: allow  Priority: 200   Enabled: yes
  Subject tags: role=data-steward
  Tool tags:    pii=true
```

`role=data-steward` users can now call PII tools; everyone else is
blocked.

#### 5. IDE-only vs automation-only (client kind)

**Goal:** automation clients (CI bots) may only hit upstreams tagged
`kind=ci`. Human IDEs (Cursor, Claude Desktop) may not touch CI
upstreams.

Tags:

- Clients: bootstrap-tagged `kind=ide` or `kind=automation` on the **MCP
  Clients** page (visible only to clients the tenant has registered).
- Upstreams: CI upstreams get `kind=ci`.

```
Policy: ide-no-ci
  Effect: deny   Priority: 80   Enabled: yes
  Client tags:   kind=ide
  Upstream tags: kind=ci

Policy: automation-only-ci
  Effect: deny   Priority: 80   Enabled: yes
  Client tags:   kind=automation
  (upstream tags empty + tool selectors empty would be ALL upstreams —
   so pin it the other way:)
  Upstream tags: kind=non-ci         # or use NOT-supported workaround:
```

Because we deliberately do **not** ship a NOT operator (non-goal), the
clean expression of "automation may only call CI" is two allow policies
under `policy_default=deny`:

```
Policy: automation-allow-ci
  Effect: allow  Priority: 90   Enabled: yes
  Client tags:   kind=automation
  Upstream tags: kind=ci

Policy: ide-allow-non-ci
  Effect: allow  Priority: 90   Enabled: yes
  Client tags:   kind=ide
  Upstream tags: kind=prod env=prod    # or whatever the IDE may reach
```

The SPA's preview pane on each policy spells out the resulting
`(client × upstream)` matrix so the admin can confirm the
intersection.

#### 6. Pinning by explicit ID (no tags needed)

**Goal:** quick fix — block one specific upstream for one specific user
without inventing tags.

The policy editor lets the admin skip every tag picker and just drop
IDs into the "Pin by ID" sections:

```
Policy: pin-block-bob-from-jira
  Effect: deny   Priority: 60   Enabled: yes
  Subject tags:  (empty)
  Upstream IDs:  ups_01HX… (jira-prod)
  Client IDs:    (empty)
  Tool patterns: (empty)
```

The editor warns: "This policy pins specific IDs. Consider tagging
instead — pins survive but become invisible if the upstream is renamed
in the future." The audit row records both the pin and the warning
ack.

Plus the matching `subjects_json` (not shown above for brevity) holds
the explicit `[usr_01HX…]` list so the policy is scoped to one user.

#### 7. Self-deny — admin locks themselves out of an upstream

**Goal:** admin Alice (also tagged `role=member`) wants the same prod
restriction her contractors have, applied to herself.

She creates / extends `contractor-deny-prod` to include
`subject_tags: role=member` and goes home. Tomorrow morning:

- Her own **Portal → Upstreams** view hides `github-prod`. ✅
- Her **Admin → Upstreams** view still shows the row (admin/owner
  always see the full inventory) with a red "denied for you" pill.
- She can still edit the policy, because Policies management is gated
  on the role, not on policy evaluation. The system is escape-hatch-
  free by design: there is no way to bypass evaluation for runtime
  calls.

#### 8. Pure starter-set flow (zero-config admin)

A brand-new admin who never opens the Policies page still gets value:

1. Tenant is provisioned. Seed runs: 7 starter tag keys, 2 disabled
   example policies, `policy_default=allow`.
2. Admin opens **Tags**, sees the keys + suggested values, applies
   `env=prod` and `env=dev` to upstreams during normal setup. No
   policies enforce anything yet.
3. Months later, when a compliance ask comes in, the admin opens
   `example-contractor-deny-prod`, reads the inline doc comment, ticks
   **Enabled**, and the deny goes live. No schema changes, no new
   tags, no rewrites.

#### 9. UX micro-flows worth calling out

- **Bulk-tagging from a list page**: `Tools → multi-select → "Add tag" →
key:value typeahead → Apply`. One audit row per binding;
  `policy.tag.bound` carries `count=N` when batched.
- **Tag rename**: changes `value` on a non-system tag. All bindings
  follow (FK to `tags.id`). Audit row keeps the prior value in
  `payload_json.before`.
- **System-tag protection**: attempting to delete a seeded `env=prod`
  tag returns a soft block — "starter tag, disable or unbind instead".
  Force-delete is gated to owner role + typed confirmation.
- **Policy preview**: every policy editor has a permanent right-hand
  pane. As the admin types selectors, the pane resolves
  `(matching_users, matching_clients, matching_upstreams,
matching_tools)` via a cheap evaluator dry-run RPC
  (`Admin.PreviewPolicy`) and shows the four counts plus a sample of
  each.
- **Test-policy console**: separate page under **Policies → Test**.
  Admin picks `(user, client, upstream, tool)` from typeaheads;
  evaluator returns the matched policy + decision + the priority/deny
  trace. The same RPC powers Staff backoffice's
  `EvaluatePolicyAsTenant`.
- **Why was I denied?**: when a user hits `policy_denied` at the MCP
  layer, the portal's **Activity** page shows the audit row with a
  human-readable summary ("You were denied by policy
  `support-readonly` because tool tag `risk=write` matched"). The
  policy name is hyperlinked only for admins.

### Using the module from code

The evaluator is a regular Go package, not a side-channel. Callers
inside Limen depend on it the same way the SPA does — via the
`policy.Evaluator` interface bound in DI.

```go
// internal/gateway/dispatch.go
decision, err := h.policy.Evaluate(ctx, policy.Request{
    UserID:         sess.UserID,
    UserTagIDs:     sess.UserTagIDs,      // cached on session
    ClientID:       sess.ClientID,
    ClientTagIDs:   sess.ClientTagIDs,
    UpstreamID:     ups.ID,
    UpstreamTagIDs: ups.TagIDs,           // cached on upstream
    ToolID:         tool.ID,
    ToolTagIDs:     tool.TagIDs,
    ToolName:       tool.Name,
})
if err != nil { return err }
if decision.Effect == policy.Deny {
    return mcprs.PolicyDenied(decision.PolicyID, decision.Reason)
}
```

Conventions:

- **Tag IDs, not strings**, on the hot path. The `Request` struct
  carries pre-resolved tag IDs; string `key=value` lookups happen
  exactly once per session/upstream/tool load and are cached. The
  evaluator never queries the DB.
- **`policy.UpstreamDenied(ctx, req)`** is the helper for "would every
  tool on this upstream be denied?". Returns `true` iff the matching
  deny has empty `tool_patterns` AND empty `tool_tags`. Visibility
  filters (`PortalService.ListMyUpstreams`, MCP config download,
  codemode `tools()`) call this; `tools/list` and `tools/call` use the
  full `Evaluate`.
- **Snapshotting**: gateway boot loads all enabled policies + the tag
  index into an immutable in-memory snapshot
  (`policy.Snapshot.Evaluate`). A pub/sub notification on
  `policy.{tag,binding,policy,default}.changed` swaps the snapshot
  atomically; in-flight requests keep the snapshot they started with.
- **Testing**: `policy.NewTestSnapshot(t, fixtures)` builds a snapshot
  from fixture structs (no DB). Every example in this section is also
  a golden test case under `internal/policy/eval_test.go`.

### Connect-RPC surface

Lives on the existing `AdminService` ([Phase 9c](phase-09c-tenant-admin-spa.md)).
No new service — DRY says one admin surface per tenant.

```proto
// proto/limen/admin/v1/admin.proto (additions)

enum TagSubjectKind { TAG_SUBJECT_USER = 0; TAG_SUBJECT_UPSTREAM = 1; TAG_SUBJECT_TOOL = 2; TAG_SUBJECT_CLIENT = 3; }

message Tag {
  string public_id   = 1;
  string key         = 2;
  string value       = 3;
  string description = 4;
  string color       = 5;
  bool   is_system   = 6;
  // binding counts split by subject kind for the Tags page
  int32  user_count     = 7;
  int32  upstream_count = 8;
  int32  tool_count     = 9;
  int32  client_count   = 10;
}

message TagSelector {
  // exactly one of:
  string tag_public_id = 1;     // exact key=value
  string key_wildcard  = 2;     // "env" → any env=*
}

message Policy {
  string public_id    = 1;
  string name         = 2;
  string description  = 3;
  PolicyEffect effect = 4;
  int32  priority     = 5;
  bool   enabled      = 6;
  bool   is_system    = 7;
  repeated TagSelector subject_tags  = 8;
  repeated TagSelector upstream_tags = 9;
  repeated TagSelector tool_tags     = 10;
  repeated TagSelector client_tags   = 11;
  repeated string upstream_public_ids = 12;
  repeated string client_public_ids   = 13;
  repeated string tool_patterns       = 14;
}

rpc ListTags(ListTagsRequest) returns (ListTagsResponse);   // optional filter by key / subject_kind
rpc CreateTag(CreateTagRequest) returns (Tag);              // key + value + description + color
rpc UpdateTag(UpdateTagRequest) returns (Tag);              // description + color only; key+value immutable
rpc DeleteTag(DeleteTagRequest) returns (google.protobuf.Empty);

rpc BindTag(BindTagRequest) returns (google.protobuf.Empty);     // tag + (user|upstream|tool|client) public_id
rpc UnbindTag(UnbindTagRequest) returns (google.protobuf.Empty);

rpc ListPolicies(google.protobuf.Empty) returns (ListPoliciesResponse);
rpc CreatePolicy(CreatePolicyRequest) returns (Policy);
rpc UpdatePolicy(UpdatePolicyRequest) returns (Policy);
rpc DeletePolicy(DeletePolicyRequest) returns (google.protobuf.Empty);
rpc SetPolicyEnabled(SetPolicyEnabledRequest) returns (Policy);

rpc GetPolicyDefault(google.protobuf.Empty) returns (PolicyDefault);
rpc SetPolicyDefault(SetPolicyDefaultRequest) returns (PolicyDefault);

rpc EvaluatePolicy(EvaluatePolicyRequest) returns (EvaluatePolicyResponse);
//   request: user_public_id, client_public_id, upstream_public_id, tool_name
//   response: effect, winning_policy (optional), matched_policies[]

// AdminService.ListUpstreams returns every upstream; each Upstream gains
//   bool denied_for_self    = N;   // self-view: this admin cannot use it
//   int32 denied_user_count = M;   // how many users in the tenant are blanket-denied
```

`PortalService` ([Phase 9b](phase-09b-portal-spa.md)) — the user-facing
listing — gains the symmetric filter:

```proto
// proto/limen/portal/v1/portal.proto
rpc ListMyUpstreams(google.protobuf.Empty) returns (ListMyUpstreamsResponse);
//   omits any upstream blanket-denied to the caller; same gate the MCP RS uses.
```

MCP-client config download endpoints (the JSON / `mcp.json` blobs the
portal hands to IDEs, [Phase 9f](phase-09f-ide-presets-and-allowlist.md))
also run through `ListMyUpstreams`-equivalent filtering — a denied
upstream must not appear in a config snippet either.

Every mutation writes one `audit_events` row (`action = "policy.<verb>"`,
see [docs/audit.md](../audit.md)). `EvaluatePolicy` is read-only and is
the same code path as the runtime gate — calling it from the admin UI
is the only way to keep "what the test says" and "what actually
happens" in lockstep.

### Staff backoffice

`StaffService` ([Phase 12](phase-12-staff-backoffice.md)) gains:

- `ListTenantPolicies(tenant_id)` — read-only, staff_mode SELECT.
- `EvaluatePolicyAsTenant(tenant_id, user_public_id, upstream, tool)` —
  same evaluator, same answer. Lets staff debug "why was Bob denied?"
  in one click from the user detail card.

Staff cannot author policies on behalf of a tenant. If a tenant is in
trouble they can impersonate ([Phase 12](phase-12-staff-backoffice.md))
and use the customer UI.

### Audit

New action verbs in [`docs/audit.md`](../audit.md) vocabulary table:

- `policy.tag.created` / `.updated` / `.deleted` (target_kind=`tag`, payload includes `key`, `value`, `is_system`)
- `policy.tag.bound` / `.unbound` (payload includes `subject_kind` and `subject_public_id`)
- `policy.created` / `.updated` / `.deleted` / `.enabled` / `.disabled`
- `policy.default.changed`
- `policy.starter_seeded` (one row per tenant on first seed, payload lists the tag keys + example policy names)
- `policy.denied` — emitted on every `tools/call` deny. This one is
  **sampled** when the same `(user, client, upstream, tool, policy_id)`
  quintuple fires within a 5-minute window, to avoid blowing up the
  audit table on a misbehaving client. Sampling state is in-process
  (LRU keyed on the quintuple). The first event in a window writes;
  subsequent events bump a counter on the cached entry; flush emits
  a single `policy.denied_burst` row at window-end.

### Performance

- Policy set per tenant: bounded at 200 in v1 (soft limit; the form
  warns at 100). Loading 200 rows of < 1 KB each is sub-millisecond.
- Tag bindings per subject (user / upstream / tool / client): bounded
  at 32. Tag IDs travel as a small `[]int64` on the request context.
  Four subject kinds × 32 tags is still a trivial slice.
- Evaluation is O(policies) per call. With the priority sort cached
  on load it short-circuits at the first match — typical tenants will
  see single-digit comparisons per tool call.
- No global mutex. Loader uses `storage.Session(ctx)` like everything
  else, and the per-request cache is request-scoped.

### Migration story

This is greenfield — no existing policy data, no v0 customers. Ship
the migration, the evaluator, the gates, the RPCs, and the SPA in one
PR. No feature flag; no `policies.enabled` config. The default
decision setting (`allow`) is the only "off switch" a tenant needs.

## Verification

- Unit: `Evaluate` table-driven across the matrix:
  empty selectors, glob patterns, priority ordering, deny-beats-allow,
  default fallthrough, both default values.
- Integration: standard `postgres:18-alpine` testcontainer
  ([AGENTS.md testing notes](../../AGENTS.md#integration-tests-with-real-postgres)).
  Tag a user, write a deny policy, hit the gateway, assert 403 +
  audit row + denied tool absent from `tools/list`.
- Cross-tenant isolation: a policy in tenant A must not influence a
  request in tenant B, including when the two tenants use tag
  `(key, value)` pairs that happen to collide. The RLS test in
  `internal/storage/storage_test.go` is the template.
- Starter seed: a freshly-provisioned tenant has the documented
  system tag set + two example policies (both `enabled=false`), and
  the seeder is idempotent on re-run.
- Tool + client gates: tagging a tool `risk=destructive` and writing
  `deny role=contractor on tool_tags=[risk=destructive]` denies the
  expected tool set across upstreams; tagging an MCP client
  `kind=automation` and writing `deny client_tags=[kind=automation]
on tool_patterns=[prod_*]` denies that client without affecting
  the same user from an `kind=ide` client.
- Codemode: a denied tool surfaces as a rejected `Promise` with
  `error.code === "policy_denied"`; the sandbox cannot read the
  underlying policy ID.
- **Whole-upstream denial uniformity**: write a deny policy with
  empty `tool_patterns` against an upstream. Assert from one fixture
  user that the upstream is absent from (a) `PortalService.ListMyUpstreams`,
  (b) the MCP-client config download payload, (c) the MCP `tools/list`
  response, (d) the `codemode.tools()` envelope, **and** (e) a direct
  `tools/call` returns `policy_denied` without leaking that the
  upstream exists. From the same tenant's admin session,
  `AdminService.ListUpstreams` still returns the row with
  `denied_for_self=true`.
- Staff read: with `WithStaffRead(ctx)` a staff session can list
  policies of every tenant; without it, it sees only its own
  (`_staff` has none).

## Checklist

- [ ] Migration `migrations/postgres/00XX_policy_engine.sql` — tables,
      enums (including the four-variant `policy_subject_kind`),
      indexes, RLS policies (with `staff_mode` clause on `SELECT`),
      minimum-shape `upstream_tools` + `mcp_clients` rows if those
      tables don't yet exist.
- [ ] GORM models in `internal/storage/models.go`:
      `Tag` (key+value), `TagBinding`, `Policy`, plus a
      `TenantSetting.PolicyDefault` column on the existing settings
      table.
- [ ] `internal/policy/seed.go` — starter system tags + two disabled
      example policies; idempotent; called from tenant-provisioning
      and as a one-shot backfill in the migration.
- [ ] `internal/policy/eval.go` — pure `Evaluate(req, policies)` with
      full unit-test coverage across all four subject kinds; plus
      `UpstreamDenied(req, policies)` helper for whole-upstream
      visibility.
- [ ] `internal/policy/loader.go` — `LoadForRequest(ctx, userID,
clientID, upstreamIDs)` returning the policy set + tag
      bindings for every subject kind on the request.
- [ ] Gateway integration:
      whole-upstream visibility filter + `tools/list` per-tool filter + `tools/call` gate, all three in `internal/mcprs/` (or wherever
      Phase 6 + 8 land the dispatch). The `tools/call` error for a
      blanket-denied upstream must be indistinguishable from the
      per-tool deny. Client identity (from the DCR’d client behind
      the token, [Phase 7b](phase-07b-dcr-per-client-project.md))
      threads into the request context.
- [ ] Codemode integration: `codemode.tools()` upstream listing drops
      hidden upstreams; alias autodiscovery
      ([Phase 8c](phase-08c-ambient-context-and-alias-discovery.md))
      does not resolve to them; `hidden_by_policy_count` populated
      on empty-filter hints; `policy_denied` error code with masked
      policy id in `internal/gateway/codemode.go`.
- [ ] PortalService.ListMyUpstreams gate + MCP-client config download
      filtering (no denied upstream surfaces in an IDE config blob).
- [ ] AdminService RPCs in `proto/limen/admin/v1/admin.proto`
      (`Tag` with key+value+is_system, `TagSelector`, four subject
      kinds on `BindTag`/`UnbindTag`, client+tool tag selectors on
      `Policy`, `denied_for_self` / `denied_user_count` on the
      Upstream message); Go + TS bindings via `buf generate`.
- [ ] Admin SPA pages: Tags (grouped by key with value chips +
      system-tag lock), Policies (list + edit with four picker rows + sample-client field in preview), Test policy (`user × client
× upstream × tool` inputs); admin Upstreams page renders the
      "Hidden from N users by policy" badge; admin Tools page + MCP
      Clients page each gain a tag picker column.
- [ ] Admin Dashboard onboarding: add a fifth `policy` bento card
      between `ide` and `invite` in [web/admin/src/pages/Dashboard.vue](../../web/admin/src/pages/Dashboard.vue);
      bump `SetupProgress` total to 5; extend the local
      `DashboardSettings` interface with `chosePolicyDefault`;
      `tenant_settings` migration adds `policy_default_chosen_at`
      (nullable timestamp, set by `SetPolicyDefault` _or_
      `MarkPolicyDefaultSkipped`, never reset); `GetTenantSettings`
      surfaces it; "Skip for now" link under the card mirrors the
      existing IDE skip pattern and stamps the timestamp with
      `payload_json.skipped=true` on the audit row.
- [ ] StaffService additions: `ListTenantPolicies`,
      `EvaluatePolicyAsTenant`. (Staff-side **tenant** tagging is
      out of scope here — tracked in
      [Phase 12](phase-12-staff-backoffice.md).)
- [ ] Audit vocabulary additions to [`docs/audit.md`](../audit.md);
      writer wired into every mutation RPC.
- [ ] `policy.denied` sampler with per-quintuple LRU + burst row on
      flush.
- [ ] Integration tests: cross-tenant isolation; default-deny tenant
      sees zero tools without an explicit allow policy; tool-tag
      gate; client-tag gate; starter-seed idempotency.
- [ ] Docs: a new `docs/policy.md` explaining the model with worked
      examples (contractor isolation by `env=prod`, read-only QA via
      `data=read-only`, automation-only `kind=automation` lockout,
      security-team-only scopes).
