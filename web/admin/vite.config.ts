import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'


// Vite config for the Limen tenant-admin SPA.
//
// base:
//   - build: "./" makes the built bundle path-relative so a single
//     artifact can be mounted under any /t/<tenant>/admin/ prefix.
//     The router reads the actual prefix from window.location.pathname
//     at boot.
//   - dev:   a static "/__vite/admin/" because Vite's dev server emits
//     absolute asset paths and the tenant slug is dynamic. Caddy
//     rewrites /t/<tenant>/admin/* to /__vite/admin/* before
//     forwarding here, and forwards bare /__vite/admin/* unchanged so
//     follow-up asset / HMR requests find us too. See
//     deploy/caddy/Caddyfile.dev.

export default defineConfig(({ command }) => ({
  base: command === 'build' ? './' : '/__vite/admin/',
  resolve: { tsconfigPaths: true },
  plugins: [vue(), tailwindcss()],
  server: {
    port: 5174,
    strictPort: true,
    // Bind on all interfaces so Caddy running in Docker can reach us
    // via host.docker.internal (the macOS default 'localhost' bind
    // only listens on the loopback adapter, which the container
    // can't route to).
    host: true,
    // Caddy fronts dev on :8000; the browser computes HMR's ws URL
    // from window.location, so point the Vite client at the same
    // origin instead of the raw :5174.
    hmr: { clientPort: 8000 },
  },
  build: {
    target: 'es2022',
    sourcemap: true,
    outDir: 'dist',
    emptyOutDir: true,
  },
}))
