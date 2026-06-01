# AGENTS.md — `scripts/stripe-bootstrap`

## What this is

A small standalone Go program that prepares Stripe billing resources for Limen.
It is **idempotent** — re-running it is always safe.

It is a separate Go module on purpose: it does not depend on any Limen
package, and it can be rebuilt without touching the main module.

## What it does

Against a fresh Stripe account, the bootstrap ensures the following exist:

| Resource | Detail |
|----------|--------|
| `Limen Developer` product | Free developer-tier product with hard limits. |
| `Limen Team` product | Paid team-tier product. Billed per active user + per concurrent SA connection. |
| `limen_developer_monthly` price | $0.01/month tracking price for the developer product. |
| `limen_team_per_active_user` price | Per-active-user price for the team product. |
| `limen_team_per_sa_connection` price | Per-SA-connection price for the team product. |
| 14 entitlements features | Limits and capabilities (max users, SA connections, upstream links, audit retention, SSO, code-mode, custom upstream, IDE presets). |
| Product-Feature attachments | Developer and Team products linked to their respective feature sets. |
| Webhook endpoint | Configured from `STRIPE_WEBHOOK_URL` with billing events. |

On success it prints, and writes to `LIMEN_BOOTSTRAP_OUT`, the values that
need to land in `.env`:

```
STRIPE_DEVELOPER_PRODUCT_ID=...
STRIPE_TEAM_PRODUCT_ID=...
STRIPE_DEV_TRACKING_PRICE_ID=...
STRIPE_TEAM_ACTIVE_USER_PRICE_ID=...
STRIPE_TEAM_SA_CONNECTION_PRICE_ID=...
STRIPE_WEBHOOK_SECRET=...
```

## How it authenticates

The bootstrap reads `STRIPE_API_KEY` from the environment. This must be a
Stripe secret key (test or live mode). Do not commit the key — it is read
from the environment at runtime.

## API style

Each helper follows the search-then-create pattern:

- **Products**: list all products, match by name. Update existing, archive
  orphans (set `active=false`), create missing.
- **Prices**: search by `lookup_key`. Create missing.
- **Features**: list all features, match by `lookup_key`. Create missing,
  update names if changed, log orphaned features with a warning.
- **Product-Features**: list attachments per product, attach missing features.
- **Webhook Endpoints**: list all endpoints, match by URL. Update events
  if changed, create missing.

## Adding a new bootstrap step

1. Add a new `ensureFoo` method on `*bootstrap` following the search-then-create
   pattern. Always treat "already exists" as success.
2. Call it from `main` after the resources it depends on.
3. If the resulting ID is needed at runtime, append it to the `out` map so
   `.bootstrap-out.env` carries it.
4. Update `docs/development.md` if the developer needs to set a new env var.

## What this is NOT

- Not part of the Limen production deploy. Production Stripe state is
  managed by this script's idempotent convergence model.
- Not a Stripe SDK. We use the official `stripe-go/v82` SDK for the few
  endpoints we need; do not grow this into a general-purpose client.
- Not on the import path of the Limen binary. Keep it that way.
- Not a general-purpose Stripe management tool — it only handles the specific resources Limen needs.
