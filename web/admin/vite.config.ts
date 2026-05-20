import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// Vite config for the Limen tenant-admin SPA.
//
// base: "./" makes the built bundle path-relative so a single artifact
// can be mounted under any /t/<tenant>/admin/ prefix. The router reads
// the actual prefix from window.location.pathname at boot.
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
    // to the Go backend on :8080. The admin SPA additionally needs
    // /t/<tenant>/admin/api/* for AdminService.
    proxy: {
      '^/t/[^/]+/(api|admin/api|auth|oauth|mcp)(/|\\?|$)': 'http://localhost:8080',
      '^/t/[^/]+/mcp-servers/[^/]+/callback(\\?|$)': 'http://localhost:8080',
      '^/\\.well-known/': 'http://localhost:8080',
      '^/auth/(login|callback|discovery)(/|\\?|$)': 'http://localhost:8080',
      '^/healthz$': 'http://localhost:8080',
    },
  },
  build: {
    target: 'es2022',
    sourcemap: true,
    outDir: 'dist',
    emptyOutDir: true,
  },
})
