import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

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
    proxy: {
      '/t': 'http://localhost:8080',
      '/auth': 'http://localhost:8080',
      '/oauth': 'http://localhost:8080',
      '/mcp': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
    },
  },
  build: {
    target: 'es2022',
    sourcemap: true,
    outDir: 'dist',
    emptyOutDir: true,
  },
})
