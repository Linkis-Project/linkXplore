#!/usr/bin/env bash
# Local deploy that ALWAYS binds 127.0.0.1:8080
# Proceeds if integration pass% >= MIN_PASS_PCT (default 50) in run-tests.sh
# macOS/bash3 compatible
set -Eeuo pipefail

log() { printf '==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
await_health() {
  host="$1"; port="$2"; timeout="${3:-60}"
  end=$(( $(date +%s) + timeout ))
  while true; do
    if curl -sSf "http://${host}:${port}/healthz" >/dev/null 2>&1; then echo "ok"; return 0; fi
    if [ "$(date +%s)" -ge "$end" ]; then echo "timeout"; return 1; fi
    sleep 1
  done
}
port_in_use() {
  _port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -iTCP:"${_port}" -sTCP:LISTEN -n -P >/dev/null 2>&1
    return $?
  fi
  if command -v nc >/dev/null 2>&1; then
    nc -z 127.0.0.1 "${_port}" >/dev/null 2>&1
    return $?
  fi
  return 1
}

MODE="${1:-local}"
[ "${MODE}" = "local" ] || die "Only 'local' mode is supported."

# Load .env if present
if [ -f ".env" ]; then
  set -a
  # shellcheck disable=SC1091
  . ".env"
  set +a
fi

IMAGE_LOCAL="txparser:test-local"
CONTAINER_NAME="solanaswap-go-txparser-1"
HOST_IP="127.0.0.1"
PORT="8080"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-15}"
CASES_FILE="${CASES_FILE:-tests/cases.json}"
MIN_PASS_PCT="${MIN_PASS_PCT:-50}"

log "local mode"
log "Config"
echo " IMAGE_LOCAL=${IMAGE_LOCAL}"
echo " CONTAINER_NAME=${CONTAINER_NAME}"
echo " HOST_IP=${HOST_IP}"
echo " PORT=${PORT} (forced)"
echo " REQUEST_TIMEOUT_SECONDS=${REQUEST_TIMEOUT_SECONDS}"
echo " CASES_FILE=${CASES_FILE}"
echo " MIN_PASS_PCT=${MIN_PASS_PCT}"

command -v docker >/dev/null 2>&1 || die "Docker not found."
docker info >/dev/null 2>&1 || die "Docker daemon not reachable."

# 1) Free 8080 from Docker containers
log "Stopping any Docker containers publishing port 8080"
busy_cids="$(docker ps -q --filter "publish=8080")"
if [ -n "${busy_cids}" ]; then
  # shellcheck disable=SC2086
  docker stop ${busy_cids} >/dev/null 2>&1 || true
fi

# Remove our final container if present
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}\$"; then
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
fi
sleep 1

# 2) If some non-Docker process owns 8080, show & abort
if port_in_use "${PORT}"; then
  log "Port 8080 is held by a non-Docker process:"
  lsof -iTCP:8080 -sTCP:LISTEN -n -P || true
  die "Release 8080 and re-run."
fi

# 3) Run the test harness (will build the image & clean up)
log "Go unit + integration tests (threshold ${MIN_PASS_PCT}%)"
if ! MIN_PASS_PCT="${MIN_PASS_PCT}" REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS}" CASES_FILE="${CASES_FILE}" ./scripts/run-tests.sh; then
  die "Integration threshold not met. (Raise MIN_PASS_PCT or fix failing cases.)"
fi

# 4) Start the FINAL container on 8080 (pass .env if present)
log "Run final container on ${HOST_IP}:${PORT}"
ENV_ARGS=()
if [ -f ".env" ]; then
  ENV_ARGS=(--env-file ".env")
fi

cid="$(docker run -d --name "${CONTAINER_NAME}" -p ${HOST_IP}:${PORT}:8080 "${ENV_ARGS[@]}" "${IMAGE_LOCAL}")" || {
  echo "---- docker ps ----"
  docker ps --format "table {{.ID}}\t{{.Names}}\t{{.Ports}}"
  die "Failed to start final container."
}

log "Container: ${cid}"
echo " Mapped: http://${HOST_IP}:${PORT}"

# 5) Health check
log "Wait for health"
if [ "$(await_health "${HOST_IP}" "${PORT}" 60)" != "ok" ]; then
  echo "---- container logs (last 200) ----"
  docker logs --tail 200 "${CONTAINER_NAME}" || true
  die "Service did not become healthy."
fi

# 6) Quick verify
log "Local health"
set +e
curl -sS "http://${HOST_IP}:${PORT}/healthz" | sed -e 's/^/ /'
set -e

log "Done. Running on http://${HOST_IP}:${PORT}"
echo
echo "Verify:"
echo " curl -i http://${HOST_IP}:${PORT}/healthz"
echo " curl -s -X POST http://${HOST_IP}:${PORT}/parse \\"
echo " -H 'Content-Type: application/json' \\"
echo " -d '{\"signature\":\"2kAW5GAhPZjM3NoSrhJVHdEpwjmq9neWtckWnjopCfsmCGB27e3v2ZyMM79FdsL4VWGEtYSFi1sF1Zhs7bqdoaVT\"}' | jq ."
echo
echo "Stop later with:"
echo " docker rm -f ${CONTAINER_NAME}"
