#!/usr/bin/env bash
# wait-for-zitadel.sh — block until Zitadel's OIDC discovery endpoint responds.
set -euo pipefail

ISSUER="${ZITADEL_ISSUER:-http://localhost:8081}"
URL="${ISSUER%/}/.well-known/openid-configuration"
TIMEOUT="${TIMEOUT:-120}"

echo "Waiting for Zitadel at ${URL} (timeout ${TIMEOUT}s)..."

start=$(date +%s)
while true; do
  if curl -fsS -o /dev/null "${URL}"; then
    echo "Zitadel is ready."
    exit 0
  fi
  now=$(date +%s)
  if (( now - start >= TIMEOUT )); then
    echo "Timed out waiting for Zitadel." >&2
    exit 1
  fi
  sleep 2
done
