# Frontend Path Alias Namespaces

This document describes the design, rationale, and usage of path alias namespaces in our frontend monorepo. It covers the problem that motivated the change, the namespace architecture, and the simplification achieved by adopting Vite 8's native `tsconfigPaths` support.

---

## Table of Contents

- [The Core Problem](#the-core-problem)
- [The Namespace Architecture](#the-namespace-architecture)
- [The Simplification: Vite 8 Native Support](#the-simplification-vite-8-native-support)
- [Maintenance & Usage Guidelines](#maintenance--usage-guidelines)
  - [Writing Imports](#writing-imports)
  - [Structuring tsconfig.json Paths](#structuring-tsconfigjson-paths)
  - [Verifying Path Resolution](#verifying-path-resolution)

---

## The Core Problem

In a pnpm workspace monorepo with multiple frontend apps (`web/portal`, `web/admin`) and a shared library (`web/shared`), overloading generic path aliases like `@/*` and `@gen/*` across packages creates three categories of failure:

### 1. Resolution Ambiguity

When `@/*` is mapped in multiple `tsconfig.json` files to different physical directories, there is no single source of truth for what `@/some-module` resolves to. The meaning changes depending on _which_ package's `tsconfig.json` a tool happens to read first. This makes imports non-deterministic and fragile.

### 2. Compiler & IDE Confusion

TypeScript, Vue Language Server, and editor tooling (VS Code, etc.) each resolve aliases independently. When the same alias points to different roots, developers see conflicting intellisense, phantom "module not found" errors, and incorrect go-to-definition behavior. The IDE may resolve `@shared/foo` correctly while the compiler does not, or vice versa.

### 3. Test Runner Crashes

Vitest maintains its own module resolution graph. When aliases are ambiguous or resolved through a custom plugin that behaves differently than the build-time resolver, tests fail with `Cannot find module` errors even though the same imports work at runtime. Keeping build and test resolution in sync becomes a manual, error-prone exercise.

---

## The Namespace Architecture

The solution is to give every import target a **unique, namespaced alias prefix** so that no alias is ever overloaded. The convention is:

| Alias | Maps To | Scope |
|---|---|---|
| `@shared/*` | `web/shared/src/*` | Cross-app shared source |
| `@shared-gen/*` | `web/shared/src/gen/*` | Cross-app generated code |
| `@/*` | `./src/*` (app-local) | App-internal source only |
| `@gen/*` | `./src/gen/*` (app-local) | App-internal generated code only |

### Design Principles

- **`@shared/` and `@shared-gen/` are global.** They always resolve to the shared library, regardless of which app imports them.
- **`@/` and `@gen/` are strictly local.** They are _only_ meaningful within the app that defines them (e.g., `web/portal/src/*` or `web/admin/src/*`). An app must never reference another app's local alias.
- **No overlap.** The prefixes are distinct at the first segment, so there is zero ambiguity about where any import originates.

---

## The Simplification: Vite 8 Native Support

Before namespacing, the monorepo relied on a custom `sharedAliasPlugin` to manually rewrite import paths at build time. This plugin was a source of complexity:

- It duplicated logic that TypeScript already understood via `tsconfig.json` `paths`.
- It required separate configuration for Vitest to mimic the same behavior.
- Any change to the shared library's structure meant updating both the plugin _and_ the tsconfig.
- Deviations between the plugin and the tsconfig caused silent bugs.

### The Pivot

Once aliases were properly namespaced and unambiguous, we could eliminate the custom plugin entirely in favor of **Vite 8's native `resolve.tsconfigPaths: true`** setting.

```ts
// vite.config.ts
export default defineConfig({
  resolve: { tsconfigPaths: true },
  // ...
})
```

### How It Works

Vite reads the consuming app's `tsconfig.json` `compilerOptions.paths` directly and resolves aliases at build time — no plugin needed. Because the same `tsconfig.json` is also referenced by Vitest:

```ts
// vitest.config.ts
export default defineConfig({
  resolve: { tsconfigPaths: true },
  // ...
})
```

...both environments use **identical, static resolution rules** derived from a single source of truth. There is no custom code to maintain, no sync risk, and no ambiguity.

### Benefits

| Before (Custom Plugin) | After (Native `tsconfigPaths`) |
|---|---|
| Custom Vite plugin to rewrite paths | Zero custom code — Vite reads `tsconfig.json` directly |
| Separate Vitest alias config needed | Same config for Vitest — one source of truth |
| fragile sync between plugin and tsconfig | Impossible to drift — both tools read the same file |
| Hard to debug resolution failures | Standard TypeScript tooling works out of the box |

---

## Maintenance & Usage Guidelines

### Writing Imports

**Importing from the shared library:**

```ts
import { useSession } from '@shared/services/session'
import { TenantCard } from '@shared/components/tenant-card.vue'
```

**Importing from shared generated code:**

```ts
import { ApiClient } from '@shared-gen/api-client'
```

**Importing app-local modules (within the same app):**

```ts
import { DashboardLayout } from '@/layouts/dashboard'
import { fetchBilling } from '@/services/billing'
```

**Importing app-local generated code:**

```ts
import { Schema } from '@gen/types'
```

### Structuring tsconfig.json Paths

Each app's `tsconfig.json` must declare the same four path mappings. The relative paths are resolved relative to the app's `tsconfig.json` location:

```jsonc
// web/portal/tsconfig.json (or web/admin/tsconfig.json)
{
  "compilerOptions": {
    "paths": {
      "@/*": ["./src/*"],
      "@gen/*": ["./src/gen/*"],
      "@shared/*": ["../shared/src/*"],
      "@shared-gen/*": ["../shared/src/gen/*"]
    }
  }
}
```

Key rules:

1. **Every app must have all four mappings.** Even if an app doesn't currently import from `@shared-gen/`, the mapping must exist so that future imports work without config changes.
2. **Paths are relative to the tsconfig file location.** `../shared/src/*` is correct because `web/portal/tsconfig.json` is one directory up from `web/shared/`.
3. **Do not add additional aliases.** If a new cross-package dependency is needed, it should live under `web/shared/src/` and be imported via `@shared/`.

### Verifying Path Resolution

1. **TypeScript compiler check:** Run `npx tsc --noEmit` from the app directory. Any unresolved alias will surface as a `TS2307: Cannot find module` error.

2. **IDE intellisense:** In VS Code, Cmd/Ctrl-click any aliased import. It should navigate directly to the correct source file. If it doesn't, restart the TypeScript/Vue language server.

3. **Vite build:** Run `pnpm build` from the app directory. A clean build confirms that Vite resolves all aliases at build time.

4. **Vitest:** Run `pnpm test` from the app directory. If any import fails during test collection, the `tsconfigPaths` resolver is likely misconfigured or the `tsconfig.json` path mapping is incorrect.

---

## Summary

| Concept | Takeaway |
|---|---|
| **Namespaced aliases** | `@shared/`, `@shared-gen/`, `@/`, `@gen/` — each maps to exactly one physical location |
| **No overloading** | `@/` is _only_ app-local; shared code always uses `@shared/` |
| **Single source of truth** | `tsconfig.json` `paths` drives resolution for TypeScript, Vite, _and_ Vitest |
| **Zero custom code** | Vite 8 `resolve: { tsconfigPaths: true }` replaces the old `sharedAliasPlugin` entirely |
