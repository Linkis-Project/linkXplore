#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
set -a; [ -f "${ROOT_DIR}/.env" ] && . "${ROOT_DIR}/.env"; set +a

echo "==> Building & starting txparser locally (compose)"
docker compose -f "${ROOT_DIR}/docker-compose.local.yml" up -d --build
echo "==> Waiting for health on 127.0.0.1:${LOCAL_TEST_PORT:-8080}"
for i in {1..40}; do
  curl -sf "http://127.0.0.1:${LOCAL_TEST_PORT:-8080}/healthz" >/dev/null && break
  sleep 0.25
done
echo "ok"