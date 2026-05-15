.PHONY: dev dev-run dev-reset dev-bootstrap dev-down build test vet fmt

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

# Migrate + serve with the full dev env auto-loaded:
#   - scripts/zitadel-bootstrap/.bootstrap-out.env (project/app/org IDs)
#   - .env.dev                                    (pinned encryption key)
#   - the admin-sa.pat read live from the docker volume
#   - dev defaults for DB DSNs / OIDC URLs / Zitadel host
# Run after `make dev` (or any time the stack is up + bootstrapped).
dev-run: .env.dev
	@test -f scripts/zitadel-bootstrap/.bootstrap-out.env || { \
		echo "missing scripts/zitadel-bootstrap/.bootstrap-out.env — run 'make dev-bootstrap'" >&2; \
		exit 1; \
	}
	@set -a; \
	  source scripts/zitadel-bootstrap/.bootstrap-out.env; \
	  source .env.dev; \
	  export LIMEN_BASE_URL=http://localhost:8080; \
	  export LIMEN_DB_DSN='postgres://limen_app:limen_app_dev@localhost:5432/limen?sslmode=disable'; \
	  export LIMEN_DB_ADMIN_DSN='postgres://limen_admin:limen_admin_dev@localhost:5432/limen?sslmode=disable'; \
	  export LIMEN_OIDC_ISSUER=http://localhost:8081; \
	  export LIMEN_OIDC_CLIENT_ID="$$LIMEN_OIDC_PORTAL_CLIENT_ID"; \
	  export LIMEN_OIDC_REDIRECT_URI=http://localhost:8080/auth/callback; \
	  export LIMEN_ZITADEL_DOMAIN=http://localhost:8081; \
	  export LIMEN_ZITADEL_AUTH_MODE=pat; \
	  export LIMEN_ZITADEL_PAT="$$(docker run --rm -v limen-dev_zitadel-bootstrap:/p:ro alpine cat /p/admin-sa.pat)"; \
	  export LIMEN_ZITADEL_PROJECT_ID="$$LIMEN_OIDC_PROJECT_ID"; \
	  export LIMEN_ZITADEL_MCP_RESOURCE_AUDIENCE="$$LIMEN_OIDC_MCP_RS_CLIENT_ID"; \
	  export LIMEN_VALKEY_ADDRESS=localhost:6380; \
	  set +a; \
	  go run ./cmd/gateway migrate && \
	  go run ./cmd/gateway serve

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
