// Zitadel Console deep-link builder.
//
// Mirrors the URL layout Zitadel renders at <issuer>/ui/console/*. All
// callers (admin Dashboard, admin Settings, admin Members) build links
// through these helpers so the issuer hostname comes from
// /auth/discovery (see ./discovery.ts) and is never hard-coded in
// components.
//
// v2 console layout notes:
//   - Org-level policy / IdP / branding tabs all live under a single
//     `/ui/console/org-settings` page with `?id=<tab>` selecting the
//     sidenav entry. Tab ids match the constants in
//     console/src/app/modules/settings-list/settings.ts (`login`,
//     `idp`, `branding`, `lockout`, …).
//   - Project-grant role assignment lives at
//     `/ui/console/granted-projects/<projectId>/grant/<grantId>` — both
//     ids are required; the page has no fallback at the project level.

export type ZitadelView =
  | "users"
  | "idp"
  | "branding"
  | "login"
  | "lockout"
  | "profile";

const VIEW_PATH: Record<ZitadelView, string> = {
  users: "/ui/console/users",
  idp: "/ui/console/org-settings?id=idp",
  branding: "/ui/console/org-settings?id=branding",
  login: "/ui/console/org-settings?id=login",
  lockout: "/ui/console/org-settings?id=lockout",
  profile: "/ui/console/users/me",
};

// zitadelConsoleUrl composes a Console URL for `view`, scoped to
// `orgId` via the `?org=` query Zitadel honours. Empty `issuer`
// returns "" so callers can render a disabled link without throwing.
export function zitadelConsoleUrl(
  issuer: string,
  orgId: string,
  view: ZitadelView,
): string {
  if (!issuer) return "";
  const base = issuer.replace(/\/+$/, "");
  const path = VIEW_PATH[view];
  if (!orgId) return `${base}${path}`;
  const sep = path.includes("?") ? "&" : "?";
  return `${base}${path}${sep}org=${encodeURIComponent(orgId)}`;
}

