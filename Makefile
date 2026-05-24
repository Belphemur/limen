.PHONY: dev dev-run hot-dev hot-dev-run hot-dev-install dev-cmd dev-migrate dev-create-tenant dev-create-upstream \
	nix-setup \
	dev-fix-bootstrap-perms \
	dev-reset dev-bootstrap dev-down dev-portal dev-portal-install dev-portal-build \
	dev-admin dev-admin-install dev-admin-build \
	build test vet fmt proto tools

# Pinned buf version. Bump deliberately; the buf.build remote plugin
# pins in buf.gen.yaml are the matching half of this contract.
BUF_VERSION := v1.69.0

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
# The bootstrap container may create this file as root:root (0600).
# Repair ownership/permissions through docker so the host user can source it.
if [ ! -r scripts/zitadel-bootstrap/.bootstrap-out.env ]; then \
	docker run --rm \
		-v "$$(pwd)/scripts/zitadel-bootstrap:/w" \
		alpine sh -c "chown $$(id -u):$$(id -g) /w/.bootstrap-out.env && chmod 600 /w/.bootstrap-out.env" >/dev/null 2>&1 || true; \
fi
test -r scripts/zitadel-bootstrap/.bootstrap-out.env || { \
	echo "scripts/zitadel-bootstrap/.bootstrap-out.env exists but is not readable; run 'make dev-fix-bootstrap-perms'" >&2; \
	exit 1; \
}
set -a
source scripts/zitadel-bootstrap/.bootstrap-out.env
source .env.dev
export LIMEN_BASE_URL=http://localhost:8000
export LIMEN_DB_DSN='postgres://limen_app:limen_app_dev@localhost:5432/limen?sslmode=disable'
export LIMEN_DB_ADMIN_DSN='postgres://limen_admin:limen_admin_dev@localhost:5432/limen?sslmode=disable'
export LIMEN_OIDC_ISSUER=http://localhost:8081
export LIMEN_OIDC_CLIENT_ID="$$LIMEN_OIDC_PORTAL_CLIENT_ID"
export LIMEN_OIDC_REDIRECT_URI=http://localhost:8000/auth/callback
export LIMEN_OIDC_POST_LOGOUT_REDIRECT_URI=http://localhost:8000/signed-out
export LIMEN_ZITADEL_DOMAIN=http://localhost:8081
export LIMEN_ZITADEL_AUTH_MODE=pat
export LIMEN_ZITADEL_PAT="$$(docker run --rm -v limen-dev_zitadel-bootstrap:/p:ro alpine cat /p/admin-sa.pat)"
export LIMEN_ZITADEL_PROJECT_ID="$$LIMEN_OIDC_PROJECT_ID"
export LIMEN_ZITADEL_MCP_RESOURCE_AUDIENCE="$$LIMEN_OIDC_MCP_RS_CLIENT_ID"
export LIMEN_VALKEY_ADDRESS=localhost:6380
export LIMEN_LOG_LEVEL=debug
export LIMEN_LOG_DEVELOPMENT=true
set +a
endef
export DEV_ENV

# Bring up the full dependency stack (Zitadel + login UI + Traefik + Postgres,
# plus Limen's own Postgres, Mailpit, Valkey), wait for it, run bootstrap,
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
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/limen migrate && go run ./cmd/limen serve'

# Bring up the full dependency stack and run the Go app with hot-reload.
# Requires air (`make hot-dev-install`).
hot-dev:
	$(COMPOSE) up -d --wait
	./scripts/wait-for-zitadel.sh
	$(MAKE) dev-bootstrap
	$(MAKE) hot-dev-run

# Migrate once, then serve via air so Go file changes trigger rebuild/restart.
hot-dev-run: .env.dev
	@bash -c 'eval "$$DEV_ENV"; \
		command -v air >/dev/null 2>&1 || { \
			echo "missing air binary; run \"make hot-dev-install\"" >&2; \
			exit 1; \
		}; \
		test -f .air.toml || { \
			echo "missing .air.toml" >&2; \
			exit 1; \
		}; \
		go run ./cmd/limen migrate; \
		exec air -c .air.toml'

hot-dev-install:
	go install github.com/air-verse/air@latest

# First-time repo bootstrap for Nix + devenv.
# Installs devenv tooling into the user profile, updates flake.lock,
# then allows the checked-in .envrc.
nix-setup:
	@bash -eu -c '\
		command -v nix >/dev/null 2>&1 || { \
			echo "nix not found. Install Nix first: https://nixos.org/download/" >&2; \
			exit 1; \
		}; \
		if command -v systemctl >/dev/null 2>&1; then \
			if systemctl is-active nix-daemon >/dev/null 2>&1; then :; else \
				echo "==========================================================================" >&2; \
				echo "WARNING: nix-daemon is not running! This usually causes permission denied." >&2; \
				echo "Run: sudo systemctl enable --now nix-daemon" >&2; \
				echo "==========================================================================" >&2; \
			fi; \
		fi; \
		nix --extra-experimental-features "nix-command flakes" profile install \
			nixpkgs#devenv \
			nixpkgs#nix-direnv \
			nixpkgs#direnv || { \
			echo "==========================================================================" >&2; \
			echo "Nix command failed. If you see permission denied on /nix/store:" >&2; \
			echo "1. On systemd Linux: sudo systemctl enable --now nix-daemon" >&2; \
			echo "2. Check if your user is in the nix-users group (usually /etc/nix/nix.conf)." >&2; \
			echo "==========================================================================" >&2; \
			exit 1; \
		}; \
		export PATH="$$HOME/.nix-profile/bin:$$PATH"; \
		nix --extra-experimental-features "nix-command flakes" flake update; \
		mkdir -p "$$HOME/.config/fish"; \
		touch "$$HOME/.config/fish/config.fish"; \
		grep -Fq "fish_add_path $$HOME/.nix-profile/bin" "$$HOME/.config/fish/config.fish" || \
			echo "if test -d $$HOME/.nix-profile/bin; fish_add_path $$HOME/.nix-profile/bin; end" >> "$$HOME/.config/fish/config.fish"; \
		grep -Fq "source $$HOME/.nix-profile/share/nix-direnv/direnvrc" "$$HOME/.config/fish/config.fish" || \
			echo "source $$HOME/.nix-profile/share/nix-direnv/direnvrc" >> "$$HOME/.config/fish/config.fish"; \
		grep -Fq "direnv hook fish | source" "$$HOME/.config/fish/config.fish" || \
			echo "direnv hook fish | source" >> "$$HOME/.config/fish/config.fish"; \
		direnv allow; \
		echo "[nix-setup] done. next: exec fish"; \
		echo "[nix-setup] then: cd . (or direnv reload)"\
	'

# Run an arbitrary limen subcommand with the dev env loaded.
# Usage: make dev-cmd ARGS="create-upstream --name foo --tenant tnt_... --url ..."
dev-cmd: .env.dev
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/limen $(ARGS)'

# Convenience wrappers — pass extra flags via ARGS=.
# Usage: make dev-create-tenant   ARGS="--name Acme --zitadel-org-id $$LIMEN_SAMPLE_TENANT_ORG_ID"
# Usage: make dev-create-upstream ARGS="--tenant tnt_... --name github --url https://api.githubcopilot.com/mcp/"
dev-migrate: .env.dev
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/limen migrate'

dev-create-tenant: .env.dev
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/limen create-tenant $(ARGS)'

dev-create-upstream: .env.dev
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/limen create-upstream $(ARGS)'

# Repair ownership + mode for bootstrap env when Docker wrote it as root.
dev-fix-bootstrap-perms:
	@docker run --rm \
		-v "$$(pwd)/scripts/zitadel-bootstrap:/w" \
		alpine sh -c "chown $$(id -u):$$(id -g) /w/.bootstrap-out.env && chmod 600 /w/.bootstrap-out.env"

# Run (or re-run) the Zitadel bootstrap, then mirror the resulting sample
# org + seed owner into Limen's own database. Both steps are idempotent.
dev-bootstrap:
	$(COMPOSE) run --rm \
		--user "$$(id -u):$$(id -g)" \
		-v "$$(go env GOMODCACHE):/go/pkg/mod" \
		-v "$$(go env GOCACHE):/tmp/go-build-cache" \
		-e HOME=/tmp \
		-e GOCACHE=/tmp/go-build-cache \
		-e GOMODCACHE=/go/pkg/mod \
		zitadel-bootstrap
	$(MAKE) dev-migrate
	@bash -c 'eval "$$DEV_ENV"; go run ./cmd/limen create-tenant \
		--name "$$LIMEN_SAMPLE_TENANT_NAME" \
		--zitadel-org-id "$$LIMEN_SAMPLE_TENANT_ORG_ID" \
		--owner-user-id "$$LIMEN_SAMPLE_OWNER_USER_ID" \
		--owner-email "$$LIMEN_SAMPLE_OWNER_EMAIL" \
		--owner-given-name Test \
		--owner-family-name Owner'

# Stop services and wipe all volumes (Limen Postgres, Zitadel Postgres, PATs).
# Also drops the pinned encryption key so the next `make dev` starts fresh.
dev-reset:
	$(COMPOSE) down -v
	rm -f scripts/zitadel-bootstrap/.bootstrap-out.env .env.dev
	rm -rf data/

dev-down:
	$(COMPOSE) down

# ---- SPA dev targets ------------------------------------------------
#
# Each SPA lives under web/<name>/. The portal SPA (Phase 9b) targets
# end-users; the admin SPA (Phase 9c) targets tenant administrators.
# Both share the same Go backend on :8080.
#
# Both bundles are reached through Caddy on http://localhost:8000:
#   /t/<tenant>/portal/  → vite (host :5173)
#   /t/<tenant>/admin/   → vite (host :5174)
# Caddy also reverse-proxies /t/<tenant>/api, /auth, /oauth, /mcp,
# /healthz, /.well-known and /signup to the Limen binary on :8080
# (started by `make dev` or `make dev-run`). Hitting :5173/:5174
# directly skips the unified origin and will break OIDC/cookie flows.
PORTAL_DIR := web/portal
ADMIN_DIR  := web/admin

# Each SPA gets its own trio of targets: install / dev / build.
dev-portal-install:
	cd $(PORTAL_DIR) && corepack pnpm install --frozen-lockfile

dev-portal:
	@if [ ! -d $(PORTAL_DIR)/node_modules ]; then \
		echo "[dev-portal] no node_modules in $(PORTAL_DIR), installing…"; \
		$(MAKE) dev-portal-install; \
	fi
	cd $(PORTAL_DIR) && corepack pnpm dev

dev-portal-build:
	cd $(PORTAL_DIR) && corepack pnpm build

# Admin SPA — same shape, different folder + port.
dev-admin-install:
	cd $(ADMIN_DIR) && corepack pnpm install --frozen-lockfile

dev-admin:
	@if [ ! -d $(ADMIN_DIR)/node_modules ]; then \
		echo "[dev-admin] no node_modules in $(ADMIN_DIR), installing…"; \
		$(MAKE) dev-admin-install; \
	fi
	cd $(ADMIN_DIR) && corepack pnpm dev

dev-admin-build:
	cd $(ADMIN_DIR) && corepack pnpm build

build:
	go build -o limen ./cmd/limen
	go build -o limenctl ./cmd/limenctl
	go build -o limen-gateway ./cmd/gateway
	go build -o limen-portal ./cmd/portal
	go build -o limen-staff ./cmd/staff

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

# Install the protobuf/Connect codegen toolchain. Buf's remote plugin
# fleet handles the language plugins themselves; we only need the buf
# CLI locally.
tools:
	go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

# Regenerate Go + TS bindings from proto/. Go output lands under
# internal/portal/portalv1/, internal/session/sessionv1/,
# internal/admin/adminv1/, internal/signup/signupv1/ (checked in);
# TS output is split per SPA scope:
#   - web/portal/src/gen/  ← PortalService bindings (gitignored)
#   - web/admin/src/gen/   ← AdminService + SignupService bindings
#                            (+ portal.v1 messages reused by admin)
#   - web/shared/src/gen/  ← SessionService bindings (gitignored,
#                            consumed via @limen/shared by every SPA)
proto:
	buf generate
	buf generate --template buf.gen.portal-ts.yaml
	buf generate --template buf.gen.admin-ts.yaml
	buf generate --template buf.gen.session-ts.yaml
