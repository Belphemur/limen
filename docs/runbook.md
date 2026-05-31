# Limen Operator Runbook

> **Status**: skeleton — covers the minimum operators need to deploy and
> maintain Limen in production. Expansion tracked in Phase 11.

## Generating the Encryption Key

```bash
# Generate a 32-byte AES-SIV key (hex-encoded)
openssl rand -hex 32
```

Set the result in `LIMEN_TOKEN_ENCRYPTION_KEY`. Store it in a secrets
manager (Vault, AWS Secrets Manager, GCP Secret Manager, or Kubernetes
Secrets) — **never** commit it to the repository or store it in plaintext
config files.

## Provisioning Postgres

Limen uses two Postgres roles:

| Role           | Purpose                          | Has BYPASSRLS? |
| -------------- | -------------------------------- | -------------- |
| `limen_admin`  | Migrations, superuser operations | Yes            |
| `limen_app`    | Runtime request-path queries     | No             |

### SQL provisioning snippet

```sql
CREATE ROLE limen_admin WITH LOGIN PASSWORD '<password>' BYPASSRLS;
CREATE ROLE limen_app WITH LOGIN PASSWORD '<password>';

CREATE DATABASE limen OWNER limen_admin;

-- Grant schema ownership to limen_admin so it can run DDL
GRANT ALL ON DATABASE limen TO limen_admin;

-- Grant connect + schema usage to limen_app
GRANT CONNECT ON DATABASE limen TO limen_app;
GRANT USAGE ON SCHEMA public TO limen_app;
```

In dev (single role), the `admin_dsn` field can be empty and falls back
to `dsn`. Production **must** set both.

## Bootstrapping Zitadel

1. Start Zitadel (see `scripts/zitadel/docker-compose.yml`).
2. Create a Limen project in Zitadel (Console → Projects → New).
3. Create two OIDC applications under the project:
   - **Portal app**: confidential client with `authorization_code` grant,
     redirect URI = `https://<limen-host>/auth/callback`.
   - **MCP RS app**: the resource server that MCP access tokens target.
4. Create a service account with a Personal Access Token (PAT) for the
   Limen management client. Grant it Org Owner on the bootstrap org.
5. Set the PAT in `LIMEN_ZITADEL_PAT`.

## Creating the First Tenant

```bash
limenctl create-tenant \
  --name "Acme Corp" \
  --zitadel-org-id "<Zitadel org ID>" \
  --owner-user-id "<Zitadel user ID>" \
  --owner-email "admin@acme.com" \
  --owner-given-name "Admin" \
  --owner-family-name "User"
```

The tenant owner can then log in at `https://<limen-host>/t/<slug>/auth/login`.

## Database Seeding (Dev & Testing)

The `seed` subcommand (`limenctl seed` or `limen seed`) populates the
database with realistic test data — a tenant, users, service accounts,
and billing history — so developers can exercise the full feature surface
without manual setup.

### Flags

| Flag           | Default                        | Description                          |
| -------------- | ------------------------------ | ------------------------------------ |
| `--tenant-id`  | `tnt_01HGPX4D1Q6G9M0C6G58V206W0` | Tenant ID to seed under            |
| `--tenant-name`| `Acme Corporation`             | Human-readable tenant name           |
| `--days`       | `30`                           | Days of billing history to generate  |
| `--users`      | `3`                            | Number of tenant users to create     |
| `--sas`        | `2`                            | Number of service accounts to create |
| `--reset`      | `false`                        | Purge and recreate tenant + dependents in clean LIFO cascade order |

### Determinism

All random data generation uses a fixed seed (`gofakeit.Seed(42)`), so
every seeding run produces **identical** data. This makes debugging
reproducible and allows test assertions against known values.

### Examples

```bash
# Standard seeding run
limenctl seed --config config.yaml --tenant-id tnt_01HGPX4D1Q6G9M0C6G58V206W0 --days 30

# Re-seeding with reset
limenctl seed --config config.yaml --reset
```

## Rotating Secrets

### Encryption key rotation

1. Decrypt all `SecretField` columns with the old key.
2. Re-encrypt with the new key.
3. Update `LIMEN_TOKEN_ENCRYPTION_KEY` and restart.
4. **Note**: encrypted columns are **unrecoverable** without the master key.

### Zitadel PAT rotation

1. Create a new PAT in Zitadel for the same service account.
2. Update `LIMEN_ZITADEL_PAT`.
3. Restart the portal binary.
4. Revoke the old PAT in Zitadel.

## Backup and Restore

### Postgres

Standard `pg_dump` / `pg_restore` of the Limen database. Encrypted columns
(`SecretField`) are backed up as ciphertext — the master encryption key is
**not** stored in the database and must be backed up separately.

1. Stop `limen-gateway`, `limen-portal`, `limen-staff`, and `limen-observer`.
2. Run `pg_dump` and save the output.
3. Restore: `pg_restore` into the target database.
4. Start `limen-gateway`, `limen-portal`, `limen-staff`, and `limen-observer`.

### Zitadel

Zitadel data (users, orgs, projects) must be backed up separately using
Zitadel's own backup procedures. The Limen database only stores references
(tenant org IDs, user IDs) — not the identity data itself.

## Monitoring

### Metrics to alert on

| Signal                        | Threshold | Severity |
| ----------------------------- | --------- | -------- |
| DCR proxy 5xx rate            | > 1%      | P2       |
| JWKS fetch failures           | Any       | P1       |
| Upstream refresh failure rate | > 10%     | P3       |
| Circuit breaker open events   | > 0       | P2       |
| Health check failures         | Any       | P1       |

### Log fields to watch

- `action=audit_event` — all audit events
- `level=error` — all error-level logs
- `name=<breaker>.state_change to=open` — breaker opened

## Health Endpoints

- `GET /healthz` — returns 200 if the server is running
- `GET /readyz` — returns 200 (readiness probes for dependency checks in future)

## Troubleshooting

### Upstream returns "circuit breaker is open"

The circuit breaker has tripped after consecutive failures. Wait for the
breaker to half-open (see `resilience.defaults.breaker_open_duration` in
config), or restart the service to reset all breakers.

### Portal login fails with "invalid state"

The OIDC state cookie may be stale or from a different tenant. Clear
cookies and retry from the correct tenant path (`/t/<slug>/auth/login`).

### "no tenant on ctx" errors

The request is missing the tenant slug in the URL path. Ensure requests
go through `/t/<tenant-slug>/` — the chi middleware extracts the tenant
from the path.
