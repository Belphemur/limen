// Tenant-scoped URL helpers shared by the portal and admin SPAs.
//
// Both SPAs are mounted under /t/<tenant>/{portal,admin}/ in production
// (Caddy file_server) and under /t/<tenant>/ in vite dev (no extra
// segment). They both also need to send the browser to the same Phase-4
// /auth/login entry point — tenant-scoped when the URL carries a tenant
// segment, tenant-agnostic otherwise.

// tenantPrefix returns the "/t/<slug>" prefix when the current location
// carries one, or null otherwise.
export function tenantPrefix(): string | null {
  const m = window.location.pathname.match(/^(\/t\/[^/]+)\//);
  return m ? m[1] : null;
}

// tenantLoginUrl builds the backend /auth/login URL for the current
// tenant, with return_to encoded as a SPA-relative path. When the URL
// has no /t/<slug>/ segment (root shell, signed-out page) it falls
// back to the tenant-agnostic /auth/login endpoint — the Phase-4
// callback resolves the tenant from the user's Zitadel home-org claim.
export function tenantLoginUrl(returnTo: string): string {
  const prefix = tenantPrefix() ?? "";
  return `${prefix}/auth/login?return_to=${encodeURIComponent(returnTo)}`;
}

// discoverSpaBasePath returns the path the SPA should pass to
// createWebHistory. spaSegment is the per-SPA folder name Caddy uses
// in production ("portal" or "admin"); the path is accepted with or
// without that segment so vite dev (bare /t/<slug>/) still works.
//
//   /t/<slug>/<spaSegment>/<route>   (production)
//   /t/<slug>/<route>                (vite dev)
//   /                                (standalone)
export function discoverSpaBasePath(spaSegment: string): string {
  const re = new RegExp(`^(\\/t\\/[^/]+\\/(?:${spaSegment}\\/)?)`);
  const m = window.location.pathname.match(re);
  return m ? m[1] : "/";
}
