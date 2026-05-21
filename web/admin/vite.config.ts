import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// Vite config for the Limen tenant-admin SPA.
//
// base: "./" makes the built bundle path-relative so a single artifact
// can be mounted under any /t/<tenant>/admin/ prefix. The router reads
// the actual prefix from window.location.pathname at boot.

// Each proxied path needs X-Forwarded-Proto/Host so the backend's
// per-request RP picker (internal/auth/oidc.go: requestOriginKey)
// matches the admin SPA's origin instead of the Vite proxy target.
const backend = {
  target: 'http://localhost:8080',
  changeOrigin: false,
  headers: {
    'X-Forwarded-Proto': 'http',
    'X-Forwarded-Host': 'localhost:5174',
  },
} as const

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
    port: 5174,
    strictPort: true,
    // Mirror the portal proxy rules — every Limen-owned route forwards
    // to the Go backend on :8080. PortalService, SessionService, and
    // AdminService are all multiplexed at /t/<tenant>/api/*.
    proxy: {
      '^/t/[^/]+/(api|auth|oauth|mcp)(/|\\?|$)': backend,
      '^/t/[^/]+/mcp-servers/[^/]+/callback(\\?|$)': backend,
      '^/\\.well-known/': backend,
      '^/auth/(login|callback|discovery|signup)(/|\\?|$)': backend,
      '^/signup(/|\\?|$)': backend,
      '^/api/limen\\.signup': backend,
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
