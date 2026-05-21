// Zitadel Console deep-link builder.
//
// Mirrors the URL layout Zitadel renders at <issuer>/ui/console/*. All
// callers (admin Dashboard, admin Settings) build links through this
// helper so the issuer hostname comes from /auth/discovery (see
// ./discovery.ts) and is never hard-coded in components.

export type ZitadelView =
  | "users"
  | "project"
  | "idp"
  | "branding"
  | "login-policy"
  | "profile";

const VIEW_PATH: Record<ZitadelView, string> = {
  users: "/ui/console/users",
  project: "/ui/console/projects",
  idp: "/ui/console/instance/idp",
  branding: "/ui/console/org/branding",
  "login-policy": "/ui/console/org/policy/login",
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
  return `${base}${path}?org=${encodeURIComponent(orgId)}`;
}
