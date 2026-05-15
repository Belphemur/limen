.PHONY: dev dev-run dev-cmd dev-migrate dev-create-tenant dev-create-upstream \
	dev-reset dev-bootstrap dev-down build test vet fmt

# Recipes use bash so `source` works.
SHELL := /bin/bash

# Compose layering: upstream Zitadel stack + Limen overlay + Limen-side services.
# All three files merge into a single "limen-dev" project.
COMPOSE := docker compose \
	--project-directory . \
	--env-file scripts/zitadel/.env \
	-f scripts/zitadel/docker-compose.yml \
	-f scripts/zitadel/docker-compose.limen.yaml \
	-f compose.dev.yaml

# Shared env-loading prelude: sources bootstrap output + .env.dev, reads the
# PAT live from the docker volume, and inlines dev defaults. Exported so
# recipes can `eval "$$DEV_ENV"` to load everything into a single bash.
define DEV_ENV
set -e
test -f scripts/zitadel-bootstrap/.bootstrap-out.env || { \
	echo "missing scripts/zitadel-bootstrap/.bootstrap-out.env — run 'make dev-bootstrap'" >&2; \
	exit 1; \
}
set -a
source scripts/zitadel-bootstrap/.bootstrap-out.env
source .env.dev
export LIMEN_BASE_URL=http://localhost:8080
export LIMEN_DB_DSN='postgres://limen_app:limen_app_dev@localhost:5432/limen?sslmode=disable'
export LIMEN_DB_ADMIN_DSN='postgres://limen_admin:limen_admin_dev@localhost:5432/limen?sslmode=disable'
export LIMEN_OIDC_ISSUER=http://localhost:8081
export LIMEN_OIDC_CLIENT_ID="$$LIMEN_OIDC_PORTAL_CLIENT_ID"
export LIMEN_OIDC_REDIRECT_URI=http://localhost:8080/auth/callback
export LIMEN_ZITADEL_DOMAIN=http://localhost:8081
export LIMEN_ZITADEL_AUTH_MODE=pat
export LIMEN_ZITADEL_PAT="$$(docker run --rm -v limen-dev_zitadel-bootstrap:/p:ro alpine cat /p/admin-sa.pat)"
export LIMEN_ZITADEL_PROJECT_ID="$$LIMEN_OIDC_PROJECT_ID"
export LIMEN_ZITADEL_MCP_RESOURCE_AUDIENCE="$$LIMEN_OIDC_MCP_RS_CLIENT_ID"
export LIMEN_VALKEY_ADDRESS=localhost:6380
set +a
endef
export DEV_ENV

# Bring up the full dependency stack (Zitadel + login UI + Traefik + Postgres,
# plus Limen's own Postgres, MailHog, Valkey), wait for it, run bootstrap,
# then migrate + serve Limen on the host with the dev env auto-loaded.
dev:
	$(COMPOSE) up -d --wait
	./scripts/wait-for-zitadel.sh
	$(MAKE) dev-bootstrap
	$(MAKE) dev-run

# Pinned encryption key (32 bytes hex). Survives across `make dev` runs so
# portal cookies + state HMAC stay valid; wiped by `make dev-reset`.
.env.dev:
	@echo "[dev] generating $@ (one-time encryption key)"
	@echo "export LIMEN_TOKEN_ENCRYPTION_KEY=$$(openssl rand -hex 32)" > $@

# Migrate + serve with the full dev env auto-loaded.
dev-run: .env.dev
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/gateway migrate && go run ./cmd/gateway serve'

# Run an arbitrary gateway subcommand with the dev env loaded.
# Usage: make dev-cmd ARGS="create-upstream --name foo --tenant tnt_... --url ..."
dev-cmd: .env.dev
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/gateway $(ARGS)'

# Convenience wrappers — pass extra flags via ARGS=.
# Usage: make dev-create-tenant   ARGS="--name Acme --zitadel-org-id $$LIMEN_SAMPLE_TENANT_ORG_ID"
# Usage: make dev-create-upstream ARGS="--tenant tnt_... --name github --url https://api.githubcopilot.com/mcp/"
dev-migrate: .env.dev
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/gateway migrate'

dev-create-tenant: .env.dev
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/gateway create-tenant $(ARGS)'

dev-create-upstream: .env.dev
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/gateway create-upstream $(ARGS)'

# Run (or re-run) the Zitadel bootstrap. Idempotent.
dev-bootstrap:
	$(COMPOSE) run --rm zitadel-bootstrap

# Stop services and wipe all volumes (Limen Postgres, Zitadel Postgres, PATs).
# Also drops the pinned encryption key so the next `make dev` starts fresh.
dev-reset:
	$(COMPOSE) down -v
	rm -f scripts/zitadel-bootstrap/.bootstrap-out.env .env.dev

dev-down:
	$(COMPOSE) down

build:
	go build -o limen ./cmd/gateway

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...
