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
// In dev the browser hits a single Caddy origin (http://localhost:8000)
// that strips /t/<tenant>/portal/ before forwarding here and reverse-
// proxies all backend routes to the Limen binary on :8080. See
// deploy/caddy/Caddyfile.dev for the full route table.

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
    // Caddy fronts dev on :8000; HMR's ws URL is derived from
    // window.location, so point the Vite client at the same origin
    // instead of the raw :5173.
    hmr: { clientPort: 8000 },
  },
  build: {
    target: 'es2022',
    sourcemap: true,
    outDir: 'dist',
    emptyOutDir: true,
  },
})
