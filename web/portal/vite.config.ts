import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// Vite config for the Limen portal SPA.
//
// base:
//   - build: "./" makes the built bundle path-relative so a single
//     artifact can be mounted under any /t/<tenant>/portal/ prefix.
//     The router reads the actual prefix from window.location.pathname
//     at boot.
//   - dev:   a static "/__vite/portal/" because Vite's dev server emits
//     absolute asset paths and the tenant slug is dynamic. Caddy
//     rewrites /t/<tenant>/portal/* to /__vite/portal/* before
//     forwarding here, and forwards bare /__vite/portal/* unchanged so
//     follow-up asset / HMR requests find us too. See
//     deploy/caddy/Caddyfile.dev.

export default defineConfig(({ command }) => ({
  base: command === 'build' ? './' : '/__vite/portal/',
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
    // Bind on all interfaces so Caddy running in Docker can reach us
    // via host.docker.internal (the macOS default 'localhost' bind
    // only listens on the loopback adapter, which the container
    // can't route to).
    host: true,
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
}))
