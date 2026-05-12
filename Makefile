.PHONY: dev dev-reset dev-bootstrap dev-down build test vet fmt

# Bring up dependency stack (Postgres x2, Zitadel, MailHog) and run Limen on host.
dev:
	docker compose -f compose.dev.yaml up -d postgres postgres-zitadel zitadel mailhog
	./scripts/wait-for-zitadel.sh
	$(MAKE) dev-bootstrap
	go run ./cmd/gateway

# Run (or re-run) the Zitadel bootstrap. Idempotent.
dev-bootstrap:
	docker compose -f compose.dev.yaml run --rm zitadel-bootstrap

# Stop services and wipe Postgres + Zitadel state.
dev-reset:
	docker compose -f compose.dev.yaml down -v
	rm -rf scripts/zitadel-bootstrap/.pat
	rm -f scripts/zitadel-bootstrap/.bootstrap-out.env

dev-down:
	docker compose -f compose.dev.yaml down

build:
	go build -o limen ./cmd/gateway

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...
