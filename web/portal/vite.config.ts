import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// Each proxied path needs X-Forwarded-Proto/Host so the backend's
// per-request RP picker (internal/auth/oidc.go: requestOriginKey)
// matches the portal SPA's origin instead of the Vite proxy target.
const backend = {
  target: 'http://localhost:8080',
  changeOrigin: false,
  headers: {
    'X-Forwarded-Proto': 'http',
    'X-Forwarded-Host': 'localhost:5173',
  },
} as const

// Vite config for the Limen portal SPA.
//
// base: "./" makes the built bundle path-relative so a single artifact
// can be mounted under any /t/<tenant>/portal/ prefix. The router reads
// the actual prefix from window.location.pathname at boot.
//
// The dev proxy forwards all backend API paths to a locally-running
// Limen binary on :8080 — Connect-RPC, OIDC, OAuth proxy, MCP, and the
// per-tenant upstream callback all need the same origin from the
// browser's perspective.
export default defineConfig({
  base: './',
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      '@gen': fileURLToPath(new URL('./src/gen', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    // Dev proxy: forward every path Limen owns to the Go process on
    // :8080. Everything else falls through to Vite's SPA history
    // fallback so Vue Router can take over.
    //
    // Keep this list in lockstep with the Caddyfile @api matcher in
    // docs/phases/phase-11-production-deployment.md — same rules,
    // expressed as glob (path) vs regex (proxy) respectively.
    //
    // Limen-owned routes:
    //   /t/<tenant>/api/*                      Connect-RPC portal API
    //   /t/<tenant>/auth/*                     per-tenant OIDC login/callback/logout
    //   /t/<tenant>/oauth/*                    OAuth AS redirector + DCR
    //   /t/<tenant>/mcp                        MCP RS root (streamable HTTP)
    //   /t/<tenant>/mcp/*                      MCP RS sub-routes (/sse, /message, PRM)
    //   /t/<tenant>/mcp-servers/<n>/callback   upstream OAuth callback (config: server.upstream_callback_path)
    //   /auth/login                            tenant-agnostic entry point
    //   /auth/callback                         OIDC RP callback
    //   /.well-known/*                         OAuth AS + OIDC + PRM discovery (strict-client variants)
    //   /healthz                               liveness
    //
    // Everything tenant-scoped under /t/<tenant>/ that is NOT listed
    // above belongs to the SPA (notably the bare /t/<tenant>/mcp-servers
    // page).
    proxy: {
      '^/t/[^/]+/(api|auth|oauth|mcp)(/|\\?|$)': backend,
      '^/t/[^/]+/mcp-servers/[^/]+/callback(\\?|$)': backend,
      '^/\.well-known/': backend,
      '^/auth/(login|callback)(/|\\?|$)': backend,
      '^/healthz$': backend,
    },
  },
  build: {
    target: 'es2022',
    sourcemap: true,
    outDir: 'dist',
    emptyOutDir: true,
  },
})
