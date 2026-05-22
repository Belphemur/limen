# Admin Frontend — Design Philosophy

> **Scope:** the tenant-admin SPA under [`web/admin/`](../web/admin/) ([Phase 9c](phases/phase-09c-tenant-admin-spa.md)).
>
> **Relationship to [`frontend-design.md`](frontend-design.md):** the shared frontend design doc remains the single source of truth for the **portal SPA** and for cross-SPA concerns (theming model, accessibility floor, Lucide icon mapping, routing shells, file layout). This document is the **admin-specific** philosophy: brand, palette, typography scale, layout proportions, and component vocabulary as they apply to the high-density admin surface. Where the two diverge for the admin SPA, **this file wins** for `web/admin/`.

---

name: Limen Admin
colors:
  surface: '#f9f9ff'
  surface-dim: '#d3daea'
  surface-bright: '#f9f9ff'
  surface-container-lowest: '#ffffff'
  surface-container-low: '#f0f3ff'
  surface-container: '#e7eefe'
  surface-container-high: '#e2e8f8'
  surface-container-highest: '#dce2f3'
  on-surface: '#151c27'
  on-surface-variant: '#444656'
  inverse-surface: '#2a313d'
  inverse-on-surface: '#ebf1ff'
  outline: '#757687'
  outline-variant: '#c5c5d8'
  surface-tint: '#2e48eb'
  primary: '#2642e6'
  on-primary: '#ffffff'
  primary-container: '#465fff'
  on-primary-container: '#f9f7ff'
  inverse-primary: '#bbc3ff'
  secondary: '#575e70'
  on-secondary: '#ffffff'
  secondary-container: '#d9dff5'
  on-secondary-container: '#5c6274'
  tertiary: '#993c00'
  on-tertiary: '#ffffff'
  tertiary-container: '#c14d00'
  on-tertiary-container: '#fff6f3'
  error: '#ba1a1a'
  on-error: '#ffffff'
  error-container: '#ffdad6'
  on-error-container: '#93000a'
  primary-fixed: '#dfe0ff'
  primary-fixed-dim: '#bbc3ff'
  on-primary-fixed: '#000d5f'
  on-primary-fixed-variant: '#0029d2'
  secondary-fixed: '#dce2f7'
  secondary-fixed-dim: '#c0c6db'
  on-secondary-fixed: '#141b2b'
  on-secondary-fixed-variant: '#404758'
  tertiary-fixed: '#ffdbcc'
  tertiary-fixed-dim: '#ffb695'
  on-tertiary-fixed: '#351000'
  on-tertiary-fixed-variant: '#7b2f00'
  background: '#f9f9ff'
  on-background: '#151c27'
  surface-variant: '#dce2f3'
  background-alt: '#F9FAFB'
  surface-white: '#FFFFFF'
  border-subtle: '#E2E8F0'
typography:
  headline-xl:
    fontFamily: Outfit
    fontSize: 32px
    fontWeight: '700'
    lineHeight: 40px
    letterSpacing: -0.02em
  headline-lg:
    fontFamily: Outfit
    fontSize: 24px
    fontWeight: '600'
    lineHeight: 32px
    letterSpacing: -0.01em
  headline-md:
    fontFamily: Outfit
    fontSize: 18px
    fontWeight: '600'
    lineHeight: 28px
  body-lg:
    fontFamily: Outfit
    fontSize: 16px
    fontWeight: '400'
    lineHeight: 24px
  body-md:
    fontFamily: Outfit
    fontSize: 14px
    fontWeight: '400'
    lineHeight: 20px
  label-sm:
    fontFamily: Outfit
    fontSize: 12px
    fontWeight: '500'
    lineHeight: 16px
    letterSpacing: 0.02em
  headline-lg-mobile:
    fontFamily: Outfit
    fontSize: 20px
    fontWeight: '600'
    lineHeight: 28px
rounded:
  sm: 0.125rem
  DEFAULT: 0.25rem
  md: 0.375rem
  lg: 0.5rem
  xl: 0.75rem
  full: 9999px
spacing:
  sidebar-width: 280px
  container-max: 1440px
  gutter: 24px
  margin-mobile: 16px
  margin-desktop: 32px

---

## Brand & Style

The design system embodies a **Corporate Modern** aesthetic tailored for high-density data management and administrative efficiency. The brand personality is professional, precise, and reliable, prioritizing functional clarity over decorative flair.

The visual direction utilizes a "Sidebar-First" architecture, characterized by a structured information hierarchy, ample whitespace within data containers, and subtle tonal shifts to differentiate navigation from content. The UI evokes a sense of organized control, making complex data sets feel approachable and actionable for power users.

## Colors

The palette is anchored by a vibrant **Electric Indigo** primary color used for actions, active states, and brand presence. This is balanced against a deep **Slate Black** for high-contrast typography and a neutral **Cool Gray** for secondary metadata.

The system relies heavily on a layered neutral scale. The main application background uses a light off-white (`#F9FAFB`) to reduce eye strain, while primary content containers use pure white (`#FFFFFF`) to create distinct separation. Borders and dividers use a subtle gray to maintain structure without introducing visual noise.

## Typography

This design system exclusively utilizes **Outfit** to leverage its geometric clarity and modern terminal endings. The type scale is optimized for dashboard environments:

- **Headlines:** Use Semi-Bold and Bold weights with tighter letter spacing for a grounded, authoritative feel.
- **Body Text:** Standardized at 14px for density, ensuring large tables and lists remain legible.
- **Labels:** Uppercase or medium weights are used for categorizing data points and form field descriptors.

## Layout & Spacing

The layout follows a **Fixed-Fluid Hybrid** model. A fixed-width sidebar (280px) persists on the left, while the main content area utilizes a fluid grid that caps at a maximum width of 1440px to prevent excessive line lengths on ultra-wide monitors.

A strict 8px spacing rhythm governs all element relationships.

- **Desktop:** 32px page margins with 24px gutters between cards/widgets.
- **Mobile:** The sidebar transitions to an off-canvas drawer, and page margins shrink to 16px.
- **Data Grids:** Use compact vertical padding (12px) to maximize information density.

## Elevation & Depth

Depth is communicated through **Tonal Layering** and **Soft Ambient Shadows**.

1. **Base Layer:** The global background (`#F9FAFB`) serves as the foundation.
2. **Surface Layer:** White cards and containers appear "raised" via a very subtle, diffused shadow (`0px 1px 3px rgba(0,0,0,0.05)`).
3. **Interactive Layer:** Hover states on buttons and clickable cards trigger a slightly deeper shadow and a border-color shift to the primary indigo.
4. **Overlay Layer:** Modals and dropdowns use a high-elevation shadow with 15% opacity to establish clear focus and separation from the underlying dashboard.

## Shapes

The shape language is **Soft** and restrained. Standard UI elements like input fields, buttons, and small cards utilize a 0.25rem (4px) radius. Larger layout containers and primary dashboard widgets use a 0.5rem (8px) radius. This balance maintains a professional, systematic appearance while avoiding the starkness of sharp corners.

## Components

- **Buttons:** Primary buttons are solid Indigo (`#465FFF`) with white text. Secondary buttons use a subtle gray border and slate text. High-density views may use "ghost" buttons for secondary actions.
- **Sidebar Nav:** Uses a dark theme (`#111827`) regardless of the main content mode. Active links feature a vertical indigo indicator and a lightened background tint.
- **Input Fields:** Outlined style with a 1px border. On focus, the border transitions to Indigo with a soft outer glow.
- **Cards:** The primary container for all dashboard content. Cards must have a white background, 4px corner radius, and a 1px border (`#E2E8F0`).
- **Data Visualization:** Charts should utilize a palette derived from the primary indigo, supplemented by a predefined set of secondary accents (Teal, Amber, Rose) for multi-series data.
- **Chips/Badges:** Small, pill-shaped indicators with low-opacity background tints (e.g., a 10% indigo background for "Active" status).

---

## Notes on divergence from [`frontend-design.md`](frontend-design.md)

The shared doc assumed "two shells, one token set" with Outfit + Inter and the `#3C50E0` brand primary. This admin philosophy supersedes those choices **for `web/admin/` only**:

| Aspect           | Shared (`frontend-design.md`) | Admin (this doc)                                                      |
| ---------------- | ----------------------------- | --------------------------------------------------------------------- |
| Primary          | `#3C50E0`                     | `#2642E6` (with `#465FFF` as `primary-container` / hover-darker)      |
| Type families    | Outfit (display) + Inter      | **Outfit only** across headlines, body, labels                        |
| Sidebar bg       | `#1C2434`                     | `#111827`                                                             |
| Active nav item  | Fully filled `bg-primary`     | Vertical indigo indicator + lightened background tint                 |
| Page background  | `#F1F5F9`                     | `#F9FAFB` (with `#F9F9FF` as the surface-tinted alternative)          |
| Token vocabulary | Flat functional names         | Material-style role tokens (`surface-container-*`, `on-surface`, ...) |

Cross-cutting concerns that **still defer to the shared doc**:

- The theming model (system / light / dark, `localStorage` key `limen.theme`, no-flash boot script).
- Accessibility floor (WCAG 2.2 AA, focus rings, keyboard nav, reduced motion).
- Icon library: **Lucide via `@lucide/vue`** — the Material Symbols names in the Stitch reference are mockup artefacts.
- Routing structure under `/t/:tenant/admin/*` and the `<AdminShell>` layout.
- Tailwind v4 `@theme` configuration in `web/admin/src/styles/main.css`; no `tailwind.config.{ts,js}`.
