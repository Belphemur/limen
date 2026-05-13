.PHONY: dev dev-reset dev-bootstrap dev-down build test vet fmt

# Compose layering: upstream Zitadel stack + Limen overlay + Limen-side services.
# All three files merge into a single "limen-dev" project.
COMPOSE := docker compose \
	--env-file scripts/zitadel/.env \
	-f scripts/zitadel/docker-compose.yml \
	-f scripts/zitadel/docker-compose.limen.yaml \
	-f compose.dev.yaml

# Bring up the full dependency stack (Zitadel + login UI + Traefik + Postgres,
# plus Limen's own Postgres and MailHog), wait for it, run bootstrap, then
# start Limen on the host.
dev:
	$(COMPOSE) up -d --wait
	./scripts/wait-for-zitadel.sh
	$(MAKE) dev-bootstrap
	go run ./cmd/gateway

# Run (or re-run) the Zitadel bootstrap. Idempotent.
dev-bootstrap:
	$(COMPOSE) run --rm zitadel-bootstrap

# Stop services and wipe all volumes (Limen Postgres, Zitadel Postgres, PATs).
dev-reset:
	$(COMPOSE) down -v
	rm -f scripts/zitadel-bootstrap/.bootstrap-out.env

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
