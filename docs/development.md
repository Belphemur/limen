# Local development

This guide gets you from a fresh clone to a Limen instance signing users in
through Zitadel — without leaving your laptop.

## Prerequisites

- Docker (with `docker compose`)
- Go 1.26+
- `curl` (for `scripts/wait-for-zitadel.sh`)

## First run

```bash
cp .env.example .env

# Brings up postgres, postgres-zitadel, zitadel, mailhog. Zitadel writes its
# initial admin PAT to scripts/zitadel-bootstrap/.pat on first start.
docker compose -f compose.dev.yaml up -d postgres postgres-zitadel zitadel mailhog
./scripts/wait-for-zitadel.sh

# Creates the Limen project, Portal + MCP RS apps, project roles, and a
# sample tenant org. Idempotent — safe to re-run.
make dev-bootstrap
```

The bootstrap script prints (and writes to
`scripts/zitadel-bootstrap/.bootstrap-out.env`):

- `LIMEN_OIDC_PORTAL_CLIENT_ID`
- `LIMEN_OIDC_MCP_RS_CLIENT_ID`
- `LIMEN_OIDC_PROJECT_ID`
- `LIMEN_SAMPLE_TENANT_ORG_ID`

Copy those values into your `.env`, then start Limen on the host:

```bash
go run ./cmd/gateway
```

Or do the whole flow in one shot:

```bash
make dev
```

## Useful URLs

| URL                                                    | What                                                |
| ------------------------------------------------------ | --------------------------------------------------- |
| http://localhost:8080                                  | Limen (this gateway)                                |
| http://localhost:8081                                  | Zitadel console (root / RootPassword1!)             |
| http://localhost:8081/.well-known/openid-configuration | OIDC discovery (Limen validates `iss` against this) |
| http://localhost:8025                                  | MailHog inbox (Zitadel sends mail here)             |
| localhost:5432                                         | Limen Postgres (user `limen`, db `limen`)           |

## Resetting

```bash
make dev-reset   # nukes both Postgres volumes and the .pat file
```

After a reset, the next `up` re-initializes Zitadel and produces a new PAT.
Re-run `make dev-bootstrap` to recreate the project/apps/org.

## Configuration

`compose.dev.yaml` is optimized for iteration (TLS off, weak secrets, ports
exposed). It is **not** suitable for production — see
[Phase 11](phases/phase-11-production-deployment.md) for the hardened stack.
