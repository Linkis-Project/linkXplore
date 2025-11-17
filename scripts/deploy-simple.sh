#!/usr/bin/env bash
set -euo pipefail

ARCHIVE="$1" # /home/azureuser/txparser-amd64.tar
LIVE_PORT="${2:-8080}"

# ensure docker daemon is up
if ! systemctl is-active --quiet docker 2>/dev/null; then
  systemctl start docker 2>/dev/null || service docker start 2>/dev/null || true
fi

echo "==> docker load"
docker load -i "$ARCHIVE" >/dev/null

# discover the just-loaded image (repo:tag)
REF=$(docker image ls --format '{{.Repository}}:{{.Tag}}' | grep -E '^txparser-local:' | head -n1)
if [[ -z "${REF}" ]]; then
  echo "ERROR: cannot find loaded txparser-local image"
  exit 1
fi
echo "Using image: $REF"

# optional env-file on the VM (synced by local-cicd.sh)
ENV_FILE="$HOME/txparser/.env"
ENV_ARGS=()
if [[ -f "$ENV_FILE" ]]; then
  ENV_ARGS=(--env-file "$ENV_FILE")
fi

echo "==> Stop & remove old"
docker rm -f txparser >/dev/null 2>&1 || true

echo "==> Run new"
docker run -d --name txparser \
  -p 0.0.0.0:${LIVE_PORT}:8080 \
  --restart unless-stopped \
  "${ENV_ARGS[@]}" \
  "$REF"

echo "==> Health"
for i in {1..40}; do
  curl -sf "http://127.0.0.1:${LIVE_PORT}/healthz" >/dev/null && break
  sleep 0.25
done
echo "OK"
