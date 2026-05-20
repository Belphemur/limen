# Frontend Design — Limen

> **Status**: design spec. Authoritative for the **portal SPA** ([Phase 9b](phases/phase-09b-portal-spa.md)) and the **tenant-admin SPA** ([Phase 9c](phases/phase-09c-tenant-admin-spa.md)). The staff backoffice ([Phase 12](phases/phase-12-staff-backoffice.md)) inherits the same tokens with a distinct shell.

This document is the **single source of truth** for Limen's visual language. It captures the design tokens, the layout shells, the component vocabulary, the icon library, the theming model (light + dark, day-one), and the accessibility floor. Anything in this file is **normative** for any Vue, CSS, or component PR.

The design is anchored on a **Stitch project** ("Limen Admin Console", project `55533304507885082`) which contains the authoritative mockups for the admin SPA. Where this doc and Stitch diverge, this doc wins — but the divergences are deliberate (Lucide instead of Material Symbols, Tailwind v4 `@theme` instead of v3 `tailwind.config`, dark mode added).

---

## 1. Design goals

| Goal                                     | Rationale                                                                                                                                                                                                                                               |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Corporate Modern, data-density-aware** | Limen is an administrative gateway. The portal is consumer-friendly but data-dense (audit, links, sessions); the admin SPA is power-user-first. The visual language prioritises legibility, alignment, and information hierarchy over decorative flair. |
| **Two shells, one token set**            | Portal is a lean top-nav app for end users. Admin is a fixed-sidebar + topbar app for tenant administrators. Both share the exact same colour, type, shape, and spacing tokens — only the shell differs. No "brand drift" between the two surfaces.     |
| **Light + dark from day one**            | Every token has a light and a dark value. The theme switcher (system / light / dark) is wired before any feature page lands so we never ship a screen that only looks right in one mode.                                                                |
| **Self-hosted everything**               | Fonts ship from `@fontsource-variable/*`; icons from `@lucide/vue`. No runtime CDN dependencies — the SPA must work behind air-gapped Caddy and across Cloudflare Pages, GitLab Pages, and self-hosted file servers without external font/icon fetches. |
| **Tailwind v4 with `@theme`**            | Tokens are defined once in `web/portal/src/styles/main.css` under `@theme { ... }`. There is **no** `tailwind.config.{ts,js}` — v4's CSS-first config is the contract. The same `main.css` (or a near-identical sibling) drives the admin SPA.          |
| **Lucide everywhere**                    | Single icon library (`@lucide/vue`), tree-shaken per icon. No Material Symbols variable font, no Heroicons mix, no inline SVG soup. New icons must come from Lucide; if Lucide lacks one, file an issue before improvising.                             |

Non-goals:

- Component library extraction (`@limen/ui`). Both SPAs are small; duplication is cheaper than a private package until phase 12.
- RTL support. English-only v1; RTL is a Phase-13 conversation tied to billing localisation.
- Animations beyond Tailwind's defaults plus `transition-colors` / `transition-transform` and a single `active:scale-95` press affordance. No Lottie, no GSAP, no entrance/exit choreography.

### 1.1 Brand mark — the Limen logo

The canonical logo is a single SVG with **no text** — a rounded blue tile containing a lean `L` monogram and a four-node connector cluster representing the gateway routing tools between clients and upstreams.

<p><img src="assets/limen-logo.svg" alt="Limen logo" width="96" height="96" /></p>

**Canonical source:** [`docs/assets/limen-logo.svg`](assets/limen-logo.svg). The portal SPA ships a byte-identical copy at [`web/portal/src/assets/limen-logo.svg`](../web/portal/src/assets/limen-logo.svg) so Vite can hash + fingerprint it as a build asset. The admin SPA will mirror the same file under `web/admin/src/assets/` when it lands. If you edit the mark, edit **both** copies in the same commit — there is no build step that syncs them.

Rules:

| Rule                | Detail                                                                                                                                                                                                                                                                                                               |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Colour              | Tile fill is the brand primary `#3c50e0`. Mark strokes/fills are pure white. The tile colour is **fixed** — it does not flip in dark mode. The mark is designed to read against both light surfaces and the dark sidebar.                                                                                            |
| Minimum size        | 20 × 20 px. Below that the connector dots collapse visually; use a wordmark or initial instead.                                                                                                                                                                                                                      |
| Topbar tile size    | Portal: 44 × 44 px inside the 64 px `h-portal-header` (`h-11 w-11 rounded-lg`) — the logo is the only brand element in the lean shell, so it gets room to breathe. Admin: 28 × 28 px inside the 80 px `h-header-height` (`h-7 w-7 rounded-md`) — paired with the sidebar brand block, the topbar mark stays compact. |
| Sidebar brand block | 32 × 32 px tile next to the "Limen Admin / Enterprise Control" stack (Stitch reference). The L-tile remains the same SVG — only the surrounding type changes.                                                                                                                                                        |
| Alt text            | When the logo sits next to the word "Limen" (topbar, sidebar header), it is decorative — pass `alt=""` and `aria-hidden="true"`. When it appears alone (favicon, OG image, splash), use `alt="Limen"`.                                                                                                               |
| Don't               | Don't recolour the tile per route/tenant. Don't replace the mark with an emoji or initial. Don't apply drop shadows or gradients. Don't render the mark on a coloured tile other than the brand primary.                                                                                                             |

Usage in a Vue SFC (Vite imports SVGs as URL strings — the file is fingerprinted into `dist/assets/`):

```vue
<script setup lang="ts">
import logoUrl from "@/assets/limen-logo.svg";
</script>

<template>
  <img
    :src="logoUrl"
    alt=""
    aria-hidden="true"
    width="28"
    height="28"
    class="h-7 w-7 rounded-md"
  />
</template>
```

The favicon, Open Graph image, and any future marketing surfaces all derive from this same SVG.

#### Favicon and install icons

The portal ships a three-file favicon set under `web/portal/public/` (Vite copies `public/` verbatim into the dist root, so the paths below are absolute from the site root):

| File                          | Purpose                                                                                                                                                                                                                                                               |
| ----------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `public/favicon.svg`          | Vector favicon used by all modern browsers. Byte-identical copy of the brand SVG; reads cleanly at any size the browser asks for.                                                                                                                                     |
| `public/favicon-32.png`       | 32 × 32 raster fallback for legacy browsers. Regenerated from `favicon.svg` with `rsvg-convert -w 32 -h 32`.                                                                                                                                                          |
| `public/apple-touch-icon.png` | 180 × 180 raster for iOS home-screen and Safari pinned-tab. Rendered from a **full-bleed** variant of the mark (no rounded corners and no transparent margin — iOS draws its own squircle mask over it; if the source were already rounded, you'd see a double-mask). |

Both PNGs are generated locally with `rsvg-convert` and committed; there is **no build step** that re-rasterises them. If you change the brand SVG, regenerate both PNGs in the same commit. Reference contract in `index.html`:

```html
<link rel="icon" href="/favicon.svg" type="image/svg+xml" />
<link rel="icon" href="/favicon-32.png" type="image/png" sizes="32x32" />
<link rel="apple-touch-icon" href="/apple-touch-icon.png" sizes="180x180" />
<meta name="theme-color" content="#3c50e0" />
```

The `theme-color` meta is the brand primary; mobile browsers paint the chrome with it. It is **not** swapped in dark mode — the brand colour is constant, same rule as the logo tile.

The admin SPA mirrors the same three files under `web/admin/public/` when it lands.

---

## 2. Tokens

Tokens live in `web/portal/src/styles/main.css` under Tailwind v4's `@theme` directive. The same block (or a re-exported partial) is mirrored in `web/admin/src/styles/main.css` when the admin SPA lands. All values below are **canonical** — derived from the Stitch design system theme `theme.designMd`, adjusted for Tailwind v4 naming and extended with dark counterparts.

### 2.1 Colour

Colour tokens map 1:1 to Tailwind utility classes (`bg-primary`, `text-on-surface`, `border-border-subtle`, …). The naming follows Material's role-based scheme (surface, on-surface, container, …) plus a few flat-named functional tokens (`success`, `danger`, `warning`).

| Token                              | Light     | Dark      | Use                                                             |
| ---------------------------------- | --------- | --------- | --------------------------------------------------------------- |
| `--color-bg-main`                  | `#F1F5F9` | `#0F1117` | Canvas behind cards (page background).                          |
| `--color-surface`                  | `#FFFFFF` | `#171A22` | Default card and panel background.                              |
| `--color-surface-container`        | `#E5EEFF` | `#1E222D` | Subtle raised tier (table headers, callouts).                   |
| `--color-surface-container-low`    | `#EFF4FF` | `#1A1E27` | Hover tier (table row hover).                                   |
| `--color-surface-container-lowest` | `#FFFFFF` | `#171A22` | Card body. Alias of `--color-surface` for clarity in templates. |
| `--color-surface-variant`          | `#D3E4FE` | `#222732` | Tonal accents inside cards.                                     |
| `--color-on-surface`               | `#0B1C30` | `#E6EAF3` | Primary text, headings.                                         |
| `--color-on-surface-variant`       | `#454655` | `#A2A8B8` | Secondary text, table cells.                                    |
| `--color-secondary`                | `#64748B` | `#94A3B8` | Muted text, helper copy.                                        |
| `--color-border-subtle`            | `#E2E8F0` | `#2A2F3B` | 1px dividers, card borders, input borders.                      |
| `--color-outline-variant`          | `#C5C5D7` | `#3A3F4D` | Decorative outlines, disabled borders.                          |
| `--color-primary`                  | `#3C50E0` | `#6B7EFF` | Brand actions, active nav, focus rings.                         |
| `--color-primary-container`        | `#465FFF` | `#3C50E0` | Hover-darker variant; sidebar CTA button.                       |
| `--color-on-primary`               | `#FFFFFF` | `#FFFFFF` | Text on primary.                                                |
| `--color-inverse-surface`          | `#213145` | `#E5E9F2` | Tooltips, snackbars.                                            |
| `--color-inverse-on-surface`       | `#EAF1FF` | `#0B1C30` | Text on `inverse-surface`.                                      |
| `--color-success`                  | `#10B981` | `#34D399` | Connected status, OK results.                                   |
| `--color-warning`                  | `#FFA70B` | `#FBBF24` | Degraded, attention.                                            |
| `--color-danger`                   | `#FB5454` | `#F87171` | Auto-disabled, destructive actions.                             |
| `--color-error`                    | `#BA1A1A` | `#F87171` | Form validation errors (alias of danger for forms).             |
| `--color-error-container`          | `#FFDAD6` | `#3C1A1A` | Inline-error background tint.                                   |
| `--color-on-error`                 | `#FFFFFF` | `#FFE4E1` | Text on `error`.                                                |

**Sidebar palette — constant across themes** (per Stitch — the sidebar is structurally dark in both modes):

| Token                            | Value                    | Use                                                  |
| -------------------------------- | ------------------------ | ---------------------------------------------------- |
| `--color-sidebar-bg`             | `#1C2434`                | Sidebar background.                                  |
| `--color-sidebar-fg`             | `#FFFFFF`                | Active item text, brand title.                       |
| `--color-sidebar-fg-muted`       | `#BEC6DC`                | Inactive item text (alias of `secondary-fixed-dim`). |
| `--color-sidebar-divider`        | `rgba(255,255,255,0.10)` | Section dividers inside sidebar.                     |
| `--color-sidebar-item-hover-bg`  | `rgba(255,255,255,0.10)` | Hover tint on inactive items.                        |
| `--color-sidebar-item-active-bg` | `var(--color-primary)`   | Active item background.                              |

Status badge convention: `bg-{token}/10 text-{token}` plus a 6px filled dot (`w-1.5 h-1.5 rounded-full bg-{token}`). Same convention in both modes; opacity stacking handles contrast.

### 2.2 Typography

Two families, self-hosted via `@fontsource-variable`:

- **Outfit** — display and headings. Geometric humanist, brand carrier. Packages: `@fontsource-variable/outfit`.
- **Inter** — body, labels, forms, tables. Workhorse. Packages: `@fontsource-variable/inter`.
- **System monospace** — `ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace`. Used for IDs, URLs, ciphertext digests, JSON snippets. No webfont — system stacks are tabular and legible everywhere.

Import in `web/{portal,admin}/src/main.ts`:

```ts
import "@fontsource-variable/inter";
import "@fontsource-variable/outfit";
```

Scale (`@theme { --font-sans: …; --text-headline-md: …; }`):

| Token         | Family     | Size      | Weight | Line height | Letter spacing | Use                                           |
| ------------- | ---------- | --------- | ------ | ----------- | -------------- | --------------------------------------------- |
| `display`     | Outfit     | 2.25 rem  | 700    | 2.5 rem     | -0.02em        | Landing hero only.                            |
| `headline-xl` | Outfit     | 2 rem     | 700    | 2.5 rem     | -0.02em        | Page-level H1.                                |
| `headline-lg` | Outfit     | 1.5 rem   | 600    | 2 rem       | normal         | Card titles, section H2.                      |
| `headline-md` | Outfit     | 1.25 rem  | 600    | 1.75 rem    | normal         | Modal titles, dashboard widget titles.        |
| `body-lg`     | Inter      | 1 rem     | 400    | 1.5 rem     | normal         | Marketing copy, long-form descriptions.       |
| `body-md`     | Inter      | 0.875 rem | 400    | 1.25 rem    | normal         | **Default body.** Tables, paragraphs, helper. |
| `label-md`    | Inter      | 0.875 rem | 600    | 1.25 rem    | normal         | Buttons, nav labels, form labels.             |
| `label-sm`    | Inter      | 0.75 rem  | 500    | 1 rem       | 0.05em         | UPPERCASE TABLE HEADERS, tag chips.           |
| `data-mono`   | mono stack | 0.875 rem | 400    | 1.25 rem    | normal         | URLs, ULIDs, digests, JSON.                   |

Numeric tables (`Tools`, `Calls`, byte counts) get `font-variant-numeric: tabular-nums` via a `.tabular-nums` utility — keep digits aligned.

### 2.3 Spacing

8 px rhythm. Tokens land in `@theme` under `--spacing-*`. The Stitch project's spacing aliases (`sidebar-width`, `header-height`, `container-padding`, `gutter`, `stack-{sm,md,lg}`) are kept verbatim because they're already referenced in mockups.

| Token                         | Value    | Use                                           |
| ----------------------------- | -------- | --------------------------------------------- |
| `--spacing-sidebar-width`     | `280px`  | Fixed sidebar width (desktop).                |
| `--spacing-header-height`     | `80px`   | Fixed topbar height (admin).                  |
| `--spacing-portal-header`     | `64px`   | Portal top-nav height (no sidebar — slimmer). |
| `--spacing-container-padding` | `2rem`   | Page padding on desktop (32 px).              |
| `--spacing-gutter`            | `1.5rem` | Grid gutter between cards (24 px).            |
| `--spacing-stack-sm`          | `0.5rem` | Tight vertical rhythm.                        |
| `--spacing-stack-md`          | `1rem`   | Default vertical rhythm.                      |
| `--spacing-stack-lg`          | `1.5rem` | Section gap.                                  |

Page padding shrinks to `1rem` (16 px) below the `md` breakpoint. Use Tailwind's `container-padding md:container-padding` pattern via `p-4 md:p-container-padding`.

### 2.4 Shape

| Token           | Value      | Use                                     |
| --------------- | ---------- | --------------------------------------- |
| `--radius-sm`   | `0.125rem` | Tag chips, inline code.                 |
| `--radius`      | `0.25rem`  | **Default** — buttons, inputs.          |
| `--radius-md`   | `0.375rem` | Secondary cards, dropdowns.             |
| `--radius-lg`   | `0.5rem`   | Primary cards, modals, sidebar items.   |
| `--radius-xl`   | `0.75rem`  | Hero panels, large feature cards.       |
| `--radius-full` | `9999px`   | Status badges, avatars, dot indicators. |

Rule: **buttons and inputs use `rounded`** (precise/administrative); **cards and major containers use `rounded-lg`** (soft, modern); **status badges use `rounded-full`** so they're visually distinct from buttons.

### 2.5 Elevation

Tonal layering does most of the work. Shadows are reserved for raised tiers.

| Level | Token            | Light shadow                                             | Dark shadow                         | Use                          |
| ----- | ---------------- | -------------------------------------------------------- | ----------------------------------- | ---------------------------- |
| L0    | (base)           | none                                                     | none                                | Page background (`bg-main`). |
| L1    | `--shadow-sm`    | `0 1px 3px rgba(0,0,0,0.05), 0 1px 2px rgba(0,0,0,0.03)` | `0 1px 3px rgba(0,0,0,0.40)`        | Cards.                       |
| L2    | `--shadow`       | `0 4px 6px -1px rgba(0,0,0,0.08)`                        | `0 4px 6px -1px rgba(0,0,0,0.50)`   | Hovered card / button.       |
| L3    | `--shadow-lg`    | `0 20px 25px -5px rgba(0,0,0,0.10)`                      | `0 20px 25px -5px rgba(0,0,0,0.60)` | Modals, dropdowns, popovers. |
| Focus | `--shadow-focus` | `0 0 0 2px rgba(60,80,224,0.20)` (primary at 20%)        | `0 0 0 2px rgba(107,126,255,0.30)`  | Focus ring overlay (see §6). |

Dark mode shadows are heavier because the background is darker — without it, raised tiers vanish.

### 2.6 Z-index ladder

Avoid arbitrary `z-50`. Use:

| Token        | Value | Use                                        |
| ------------ | ----- | ------------------------------------------ |
| `z-base`     | `0`   | Default flow.                              |
| `z-sticky`   | `10`  | Sticky table headers, in-page sticky bars. |
| `z-sidebar`  | `20`  | Fixed sidebar.                             |
| `z-topbar`   | `40`  | Fixed topbar.                              |
| `z-dropdown` | `50`  | Dropdowns, popovers, tooltips.             |
| `z-modal`    | `60`  | Modal backdrop + dialog.                   |
| `z-toast`    | `70`  | Toasts / snackbars.                        |

---

## 3. Theming — light, dark, day-one

### 3.1 Mode contract

Three user-selectable modes:

| Mode     | Behaviour                                                                                                                   |
| -------- | --------------------------------------------------------------------------------------------------------------------------- |
| `system` | Follow `prefers-color-scheme`. Live-update when the OS toggles (use a `matchMedia` listener). **Default for new sessions.** |
| `light`  | Force light regardless of OS.                                                                                               |
| `dark`   | Force dark regardless of OS.                                                                                                |

The chosen mode is persisted to `localStorage` under the key `limen.theme` (values: `"system"` | `"light"` | `"dark"`). Both SPAs read the same key — switching mode in the portal sticks when the user opens the admin SPA in the same browser.

### 3.2 Tailwind v4 wiring

Tailwind v4 dark mode runs through a class variant. We use a **`.dark` class on `<html>`** rather than the media-query strategy because we need to support the explicit `light` / `dark` overrides.

In `web/portal/src/styles/main.css`:

```css
@import "tailwindcss";

@custom-variant dark (&:where(.dark, .dark *));

@theme {
  /* colour tokens — light defaults */
  --color-bg-main: #f1f5f9;
  --color-surface: #ffffff;
  /* … the full table from §2.1 … */

  /* spacing, radius, font tokens */
  --spacing-sidebar-width: 280px;
  --spacing-header-height: 80px;
  --radius: 0.25rem;
  --radius-lg: 0.5rem;
  --font-sans: "Inter Variable", ui-sans-serif, system-ui, sans-serif;
  --font-display: "Outfit Variable", ui-sans-serif, system-ui, sans-serif;
  --font-mono:
    ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
}

/* dark overrides — same tokens, dark values */
.dark {
  --color-bg-main: #0f1117;
  --color-surface: #171a22;
  /* … */
}

:root {
  color-scheme: light dark;
}
html,
body,
#app {
  min-height: 100vh;
}
```

The `@custom-variant dark` line teaches Tailwind to emit `.dark ` ancestor selectors when you write `dark:bg-surface`. Without it, v4 falls back to the prefers-color-scheme variant which we explicitly don't want.

### 3.3 Pinia store

`web/portal/src/stores/theme.ts`:

```ts
import { defineStore } from "pinia";

type Mode = "system" | "light" | "dark";
const STORAGE_KEY = "limen.theme";

export const useTheme = defineStore("theme", {
  state: () => ({ mode: "system" as Mode }),
  getters: {
    effective(state): "light" | "dark" {
      if (state.mode !== "system") return state.mode;
      return window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
    },
  },
  actions: {
    init() {
      const saved =
        (localStorage.getItem(STORAGE_KEY) as Mode | null) ?? "system";
      this.mode = saved;
      this.apply();
      // Live-react to OS changes when in system mode.
      window
        .matchMedia("(prefers-color-scheme: dark)")
        .addEventListener("change", () => {
          if (this.mode === "system") this.apply();
        });
    },
    set(next: Mode) {
      this.mode = next;
      localStorage.setItem(STORAGE_KEY, next);
      this.apply();
    },
    apply() {
      document.documentElement.classList.toggle(
        "dark",
        this.effective === "dark",
      );
    },
  },
});
```

Call `useTheme().init()` once in `main.ts` immediately after `app.use(pinia)`.

### 3.4 No-flash boot script

Inline in `index.html` **before** any other script — runs synchronously to set the `.dark` class before the SPA paints:

```html
<script>
  (function () {
    try {
      var m = localStorage.getItem("limen.theme") || "system";
      var dark =
        m === "dark" ||
        (m === "system" &&
          window.matchMedia("(prefers-color-scheme: dark)").matches);
      if (dark) document.documentElement.classList.add("dark");
    } catch (_) {
      /* SSR / private mode — ignore */
    }
  })();
</script>
```

This is the **only** inline script we accept; everything else lives in modules. The CSP `script-src` directive must allow a hash for this one (computed at build time).

### 3.5 Theme switcher placement

- **Portal** — in the user-menu popover (top-right avatar), as a three-segment toggle "System · Light · Dark".
- **Admin** — same three-segment toggle in the topbar utility cluster (between the notifications and avatar buttons), so power users hit it in one click.

Component: `<ThemeSwitcher />` in `web/portal/src/components/`. Built from three `<button>`s in a `role="radiogroup"` for accessibility (see §6).

---

## 4. Layout shells

### 4.1 Portal shell — lean, top-nav only

The portal (Phase 9b) is a lightweight end-user surface. No sidebar. The shell:

```
┌─────────────────────────────────────────────────────────────────┐
│  [Logo] Limen                          Docs   Status   [Avatar▾] │  64 px topbar
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  <main class="max-w-screen-xl mx-auto p-4 md:p-container-padding">
│    page content                                                 │
│  </main>                                                        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

- Topbar: `h-portal-header bg-surface border-b border-border-subtle`, sticky at top. Logo left, links centred-right (`Docs`, `Status`), avatar dropdown far right with: "My activity", "MCP servers", "Theme: …", "Sign out".
- Content: `max-w-screen-xl` so long pages don't span 4K monitors. Same `p-4 md:p-container-padding` rhythm as admin.
- No collapsing animation, no mobile hamburger overlay (links collapse into the avatar menu under `md`).

### 4.2 Admin shell — sidebar + topbar

The admin SPA (Phase 9c) ships the full Stitch sidebar:

```
┌─[ Sidebar 280px ──┐┌─[ Topbar 80px ─────────────────────────────┐
│ [L] Limen Admin   ││  🔍 search…       Docs  API Status  🔔 ⚙  👤│
│     Enterprise…   │├────────────────────────────────────────────┤
│ ━━━━━━━━━━━━━━━━━│ ←Section divider
│ ▣ Dashboard       │ │  ┌─ Page header ────────────────────────┐
│ 🧠 LLM         ▾  │ │  │ MCP Upstream Management              │
│   • MCP Servers   │ │  │ Manage and monitor connected …       │
│ 🏢 Organization▾  │ │  └──────────────────────────────────────┘
│   • Settings      │ │
│   • Users & Roles │ │  ┌─ Card ─────────────────────────────  │
│                   │ │  │ [toolbar] [filter] [sort] [export]   │
│ ━━━━━━━━━━━━━━━━━│ │  │ ┌─ Table ───────────────────────────┐│
│ [+ Add New Server]│ │  │ │ NAME  URL  STATUS  TOOLS  ACTIONS ││
│ ⚙  Settings       │ │  │ │ …                                 ││
│ ⏻ Logout          │ │  │ └───────────────────────────────────┘│
│                   │ │  └──────────────────────────────────────┘
└───────────────────┘└─────────────────────────────────────────────
```

#### Sidebar — definitive structure (locked from Stitch "Admin Dashboard (New Nav)")

```
[Brand]    L-tile (primary-container bg, white "L") + "Limen Admin" / "Enterprise Control"

[Nav]
├─ Dashboard                                 (Lucide: LayoutDashboard)
├─ LLM ▾                                     (Lucide: Brain)
│   └─ MCP Servers                                                  → /t/{tenant}/admin/mcp-servers
└─ Organization ▾                            (Lucide: Building2)
    ├─ Organization Settings                                        → /t/{tenant}/admin/organization
    └─ Users & Roles                                                → /t/{tenant}/admin/users

[Section divider — border-t border-sidebar-divider, mt-auto]

[Footer]
├─ [Primary CTA full-width] + Add New Server  (Lucide: Plus)        → modal: AddMcpServer
├─ Settings                                   (Lucide: Settings)    → /t/{tenant}/admin/settings
└─ Logout                                     (Lucide: LogOut)      → /t/{tenant}/auth/logout
```

Token rules for the sidebar:

- Background: `bg-sidebar-bg` (constant `#1C2434` in both themes).
- Brand tile (the "L"): `w-10 h-10 rounded-lg bg-primary-container text-white font-display`.
- Items: `text-sidebar-fg-muted hover:text-sidebar-fg hover:bg-sidebar-item-hover-bg`, padding `px-4 py-3`, `rounded-lg`, `gap-3` between icon and label.
- **Active** item: `bg-primary text-white` (no underline, no left bar — fully filled).
- Group nodes: `<button>` with chevron (`ChevronDown` Lucide, rotates 180° when open). Children indent `pl-11`, smaller text `text-body-md`, hover `hover:text-sidebar-fg` only (no bg tint on children).
- Footer separator: `border-t border-sidebar-divider mt-auto pt-stack-md`.
- "Add New Server" CTA: `bg-primary-container hover:bg-primary text-white w-full rounded-lg shadow-sm` (uses `--shadow-sm`).
- Mobile (`<md`): the entire sidebar slides off-canvas. A hamburger button in the topbar toggles it. Backdrop is `bg-on-surface/40` and dismissable by click.

#### Topbar — admin

`h-header-height bg-surface border-b border-border-subtle px-gutter` with three clusters:

1. **Left**: global search — full-width input up to `max-w-md`, leading `Search` Lucide icon, placeholder _"Search servers, tools, or resources…"_. Submits to a future global-search route (Phase 12).
2. **Centre-right** (`hidden md:flex`): `Docs`, `API Status` text links.
3. **Right**: notifications bell (`Bell` Lucide) with a 6 px primary dot when unread; settings (`Settings` Lucide) → `/admin/settings`; `<ThemeSwitcher />`; avatar button → user-menu popover.

The topbar is `position: fixed` with `left: 280px; width: calc(100% - 280px)` on `md+`. Below `md` it spans full width and the hamburger button replaces the search collapse.

#### Page region

- `<main class="ml-0 md:ml-sidebar-width mt-header-height">`
- Inner wrapper: `max-w-[1440px] mx-auto p-4 md:p-container-padding`
- Default page header pattern: H1 (`headline-md`) + secondary description (`body-md text-secondary`) on the left, primary action button on the right, separated by `mb-8`.

---

## 5. Component vocabulary

All components live in `web/{portal,admin}/src/components/`. **No external component library.** Headless primitives where needed come from the standard browser primitives and our own thin wrappers — we are not adopting Radix/Reka/Headless UI for v1 because the surface is small enough to hand-roll cleanly. Reconsider when component count > ~25.

### 5.1 Buttons

| Variant       | Classes                                                                                                                                                   | Use                         |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------- |
| `primary`     | `bg-primary text-on-primary hover:bg-primary-container active:scale-[0.98] shadow-sm rounded px-4 py-2 font-label-md text-label-md`                       | Default action per surface. |
| `secondary`   | `bg-surface text-on-surface border border-border-subtle hover:bg-surface-container-low active:scale-[0.98] rounded px-4 py-2 font-label-md text-label-md` | Cancel, secondary CTA.      |
| `ghost`       | `text-secondary hover:text-on-surface hover:bg-surface-container-low active:scale-[0.98] rounded px-3 py-2 font-label-md text-label-md`                   | Tertiary inline actions.    |
| `destructive` | `bg-danger text-on-error hover:opacity-90 active:scale-[0.98] rounded px-4 py-2 font-label-md text-label-md`                                              | Force-unlink, revoke, etc.  |
| `icon`        | `w-10 h-10 rounded-full text-on-surface-variant hover:bg-surface-container-high active:opacity-80 inline-flex items-center justify-center`                | Topbar utility buttons.     |

All variants honour `disabled:opacity-60 disabled:pointer-events-none`. Loading spinners replace the leading icon, not the label.

### 5.2 Inputs

```html
<label class="block">
  <span class="font-label-md text-label-md text-on-surface mb-1.5 block"
    >Server name</span
  >
  <input
    class="w-full bg-surface border border-border-subtle rounded px-3 py-2
           text-body-md font-body-md text-on-surface placeholder:text-secondary
           focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20
           disabled:bg-surface-container-low disabled:text-secondary
           aria-[invalid=true]:border-danger aria-[invalid=true]:ring-danger/20"
    type="text"
  />
  <p class="font-label-sm text-label-sm text-danger mt-1.5" v-if="error">
    {{ error }}
  </p>
</label>
```

Label above field (never floating). Errors live directly below the input in `text-danger`. Helper text uses `text-secondary`.

### 5.3 Cards

```html
<section class="bg-surface rounded-lg border border-border-subtle shadow-sm">
  <header
    class="px-6 py-4 border-b border-border-subtle flex items-center justify-between"
  >
    <h2 class="font-display text-headline-md text-on-surface">Card title</h2>
    <!-- card-level actions -->
  </header>
  <div class="p-6">…</div>
</section>
```

`p-6` (= 1.5 rem = 24 px) is the canonical card padding. Multi-section cards get an inner `<header>` with bottom border; single-section cards omit it.

### 5.4 Data tables

```html
<div class="overflow-x-auto">
  <table class="w-full text-left border-collapse">
    <thead>
      <tr class="bg-surface-container border-b border-border-subtle">
        <th
          class="px-6 py-3 font-label-sm text-label-sm uppercase tracking-wider text-secondary"
        >
          Server
        </th>
        …
      </tr>
    </thead>
    <tbody class="divide-y divide-border-subtle font-body-md text-body-md">
      <tr class="hover:bg-surface-container-low transition-colors group">
        <td class="px-6 py-4 text-on-surface">…</td>
        …
      </tr>
    </tbody>
  </table>
</div>
```

Rules:

- Horizontal dividers only (`divide-y divide-border-subtle`). No vertical rules.
- Headers `uppercase tracking-wider` (`label-sm`).
- Numeric cells get `.tabular-nums`.
- URL / ID cells get `font-mono text-data-mono`.
- Row hover tint: `hover:bg-surface-container-low`.
- Row-level actions live in a `text-right` last column. Use `icon` buttons for the action set.

### 5.5 Status badges

```html
<span
  class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full font-label-sm text-label-sm
             bg-success/10 text-success"
>
  <span class="w-1.5 h-1.5 rounded-full bg-success"></span>
  Connected
</span>
```

Token map:

| Status         | Token       | Example uses                             |
| -------------- | ----------- | ---------------------------------------- |
| Connected / OK | `success`   | Upstream healthy, breaker closed.        |
| Degraded       | `warning`   | Refresh-failed streak < auto-disable.    |
| Auto-disabled  | `danger`    | Phase 7 auto-disable, breaker tripped.   |
| Pending        | `secondary` | OAuth flow not yet completed.            |
| Encrypted      | `primary`   | Audit-row "encrypted payload" indicator. |

### 5.6 Dialogs / modals

- Backdrop: `bg-on-surface/40 dark:bg-on-surface/60 backdrop-blur-sm z-modal`.
- Panel: `bg-surface rounded-lg shadow-lg max-w-lg w-full border border-border-subtle`.
- Dismissal: backdrop click + ESC + explicit close button (Lucide `X` in the top-right).
- Focus trap: trap focus inside the panel for the lifetime of the dialog. Use a tiny in-house composable (`useFocusTrap`) — no library.

### 5.7 Empty states

```html
<div
  class="flex flex-col items-center justify-center text-center p-12 border-2 border-dashed
            border-border-subtle rounded-lg text-secondary"
>
  <Inbox class="w-12 h-12 mb-4 opacity-50" />
  <h3 class="font-display text-headline-md text-on-surface mb-1">
    No upstreams yet
  </h3>
  <p class="font-body-md text-body-md mb-6 max-w-md">
    Connect an MCP server to start aggregating tools for your tenant.
  </p>
  <button class="…primary button…">Add new server</button>
</div>
```

Dashed border + reduced opacity icon — distinguishes "empty by design" from "loading" or "error".

### 5.8 Toasts

Top-right stack, `z-toast`. Each toast: `bg-surface border-l-4 border-{token} rounded shadow-lg p-4 pr-12 max-w-sm`. Auto-dismiss after 5 s (success) or 10 s (error). The icon to the left of the title matches the token (Lucide `CheckCircle2` / `AlertTriangle` / `XCircle`).

---

## 6. Accessibility floor (WCAG 2.2 AA)

| Requirement        | Implementation                                                                                                                                                                                             |
| ------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Contrast**       | All colour combinations above are tested ≥ 4.5:1 for body text, ≥ 3:1 for large text and UI components. Dark-mode pairings re-tested with the same thresholds.                                             |
| **Focus rings**    | Use `focus:ring-2 focus:ring-primary/20 focus:outline-none focus:border-primary` on all interactive elements. Never `outline: none` without a replacement ring.                                            |
| **Keyboard nav**   | Sidebar groups: Enter/Space toggles. Arrow keys move within a group. Tab order = visual order. Skip-link at the top of `<main>` (`<a href="#main" class="sr-only focus:not-sr-only">Skip to content</a>`). |
| **Theme switcher** | `role="radiogroup"` on the wrapper, `role="radio" aria-checked="…"` on each button. Arrow-left/right cycles options.                                                                                       |
| **Forms**          | Every input has a `<label>`. Errors associated via `aria-describedby`. Required fields: `aria-required="true"` + visible asterisk.                                                                         |
| **Modals**         | `role="dialog" aria-modal="true" aria-labelledby="…title-id"`. Focus trap + body `overflow: hidden` while open. Restore focus to the trigger on close.                                                     |
| **Toasts**         | `role="status"` (info/success) or `role="alert"` (error). Live region announces on mount.                                                                                                                  |
| **Reduced motion** | Wrap any `transition-*` longer than 200 ms in `motion-safe:`. Respect `prefers-reduced-motion: reduce` — no `active:scale-95` for those users.                                                             |
| **Icons**          | All `<lucide-icon>` components used decoratively get `aria-hidden="true"`. Icons that carry meaning (status, action-without-label) get `aria-label="…"`.                                                   |

---

## 7. Icons — Lucide

Package: `@lucide/vue` (the maintained successor to the deprecated `lucide-vue-next`). Each icon imported by name; the bundler tree-shakes the rest.

```vue
<script setup lang="ts">
import {
  LayoutDashboard,
  Brain,
  Building2,
  Plus,
  Settings,
  LogOut,
} from "@lucide/vue";
</script>

<template>
  <LayoutDashboard :size="20" aria-hidden="true" />
</template>
```

Mapping from the Stitch Material-Symbols mockups to Lucide:

| Material Symbol            | Lucide                     | Use                                 |
| -------------------------- | -------------------------- | ----------------------------------- |
| `dashboard`                | `LayoutDashboard`          | Sidebar — Dashboard.                |
| `psychology`               | `Brain`                    | Sidebar — LLM group.                |
| `corporate_fare`           | `Building2`                | Sidebar — Organization group.       |
| `dns`                      | `Server`                   | MCP Servers child / table row icon. |
| `settings_input_component` | `SlidersHorizontal`        | Organization Settings.              |
| `group`                    | `Users`                    | Users & Roles.                      |
| `add`                      | `Plus`                     | "+ Add" CTAs.                       |
| `expand_more` / `chevron`  | `ChevronDown`              | Group expand chevrons.              |
| `notifications`            | `Bell`                     | Topbar notifications.               |
| `settings`                 | `Settings`                 | Topbar settings, sidebar footer.    |
| `logout`                   | `LogOut`                   | Sidebar footer.                     |
| `search`                   | `Search`                   | Topbar / table search leading icon. |
| `filter_list`              | `Filter`                   | Table filter input.                 |
| `sort`                     | `ArrowUpDown`              | Table sort toggle.                  |
| `download`                 | `Download`                 | Table export.                       |
| `more_vert`                | `MoreVertical`             | Row-level overflow menu.            |
| `check_circle`             | `CheckCircle2`             | Success toast / status.             |
| `warning`                  | `AlertTriangle`            | Warning toast / status.             |
| `error`                    | `XCircle` / `AlertOctagon` | Error toast / status.               |
| `help`                     | `LifeBuoy`                 | Support / help links.               |
| `refresh`                  | `RefreshCw`                | Manual refresh actions.             |

Icon sizing convention: **`size={20}` for inline icons** (sidebar items, table actions), **`size={24}` for topbar utility buttons**, **`size={48}` for empty-state hero icons**. No arbitrary sizes.

---

## 8. Routing & shell wiring

### 8.1 Portal routes (lean)

```
/                            → portal home (signed-out)
/auth/login                  → 302 → Zitadel (backend, see phase-09b)
/t/:tenant                   → tenant landing
/t/:tenant/mcp-servers       → portal upstreams list (end-user view)
/t/:tenant/mcp-servers/:id   → upstream detail
/t/:tenant/me                → "my activity" + sessions
```

Layout: `<PortalShell>` wraps all routed views — topbar + `<router-view>`.

### 8.2 Admin routes (sidebar)

```
/t/:tenant/admin                          → redirect to /dashboard
/t/:tenant/admin/dashboard                → admin overview
/t/:tenant/admin/mcp-servers              → list (table view, ref: Stitch "MCP Upstream Management")
/t/:tenant/admin/mcp-servers/new          → wizard (ref: "Add New MCP Server")
/t/:tenant/admin/mcp-servers/:id          → detail + tool inventory
/t/:tenant/admin/organization             → settings (ref: "Organization Settings")
/t/:tenant/admin/users                    → users & roles (ref: "Users & Roles")
/t/:tenant/admin/audit                    → audit log (ref: "Audit Log")
/t/:tenant/admin/settings                 → per-user preferences
```

Layout: `<AdminShell>` wraps all admin routed views — sidebar + topbar + `<router-view>` inside the main region.

The sidebar's active-item logic comes from `vue-router`'s active-route matching (`router-link-active` is wired by the framework). Expand state for group nodes is **derived from the active route** on mount, then user-controlled — collapsing a group with an active child is allowed; the active styling on the child remains visible when expanded.

---

## 9. File layout

```
web/portal/                          # phase-09b — lean portal SPA
  src/
    styles/main.css                  # @theme tokens, dark variant, base
    components/
      AppButton.vue
      AppInput.vue
      AppCard.vue
      AppDialog.vue
      StatusBadge.vue
      ThemeSwitcher.vue
      PortalShell.vue                # topbar + <router-view>
      icons/index.ts                 # named Lucide re-exports
    stores/theme.ts
    main.ts
    App.vue

web/admin/                           # phase-09c — tenant-admin SPA (future)
  src/
    styles/main.css                  # imports the same tokens; adds admin-only
    components/
      AdminShell.vue                 # sidebar + topbar + <router-view>
      sidebar/
        SidebarNav.vue
        SidebarGroup.vue
        SidebarItem.vue
      topbar/
        TopbarSearch.vue
        TopbarUtilityCluster.vue
    # AppButton / AppInput / AppCard / AppDialog / StatusBadge / ThemeSwitcher
    # are duplicated until volume justifies a shared package.
```

Both SPAs run independently (separate `pnpm` projects under `web/`). They share **tokens** (kept in sync by code review against this file), not source.

---

## 10. Worked example — sidebar group node

```vue
<!-- web/admin/src/components/sidebar/SidebarGroup.vue -->
<script setup lang="ts">
import { ref, computed } from "vue";
import { useRoute } from "vue-router";
import { ChevronDown } from "lucide-vue-next";
import type { Component } from "vue";

const props = defineProps<{
  label: string;
  icon: Component;
  routePrefix: string;
}>();
const route = useRoute();

const containsActive = computed(() => route.path.startsWith(props.routePrefix));
const open = ref(containsActive.value);
</script>

<template>
  <div class="space-y-1">
    <button
      class="w-full flex items-center gap-3 px-4 py-3 rounded-lg justify-between
             text-sidebar-fg-muted hover:text-sidebar-fg hover:bg-sidebar-item-hover-bg
             transition-colors duration-200"
      :aria-expanded="open"
      @click="open = !open"
    >
      <div class="flex items-center gap-3">
        <component :is="icon" :size="20" aria-hidden="true" />
        <span class="font-label-md text-label-md">{{ label }}</span>
      </div>
      <ChevronDown
        :size="16"
        aria-hidden="true"
        class="transition-transform duration-200"
        :class="{ 'rotate-180': open }"
      />
    </button>
    <div v-show="open" class="pl-11 pr-4 space-y-1">
      <slot />
    </div>
  </div>
</template>
```

Children are plain `<router-link>`s styled with `block py-2 text-sidebar-fg-muted hover:text-sidebar-fg font-body-md text-body-md` plus an active-route override `router-link-active:text-sidebar-fg router-link-active:font-label-md`.

---

## 11. Open changes already on the radar

These are flagged so reviewers don't think they're missing:

- **Portal currently has no `ThemeSwitcher` or sidebar.** Phase 9b PR 8 (in-flight) adds the theme switcher into the avatar menu and lays the dark tokens; this doc is the prerequisite.
- **`tailwindcss.config.{ts,js}` does not exist** and must not be re-introduced. All tokens belong in `@theme` in `main.css`.
- **Material Symbols stylesheets in the Stitch mockup HTML are an artefact of the mockup tool.** Real implementation uses Lucide. The mapping table in §7 is the migration key.
- **The "Audit Log" Stitch screen** uses a card grid plus a paginated table; that page is owned by [Phase 12 — staff backoffice](phases/phase-12-staff-backoffice.md) once the staff shell exists, with a tenant-scoped projection landing earlier in Phase 9c.
