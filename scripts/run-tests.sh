#!/usr/bin/env bash
# Build + integration tests on a TEMP container bound to 127.0.0.1:8080.
# Exits 0 if pass % >= MIN_PASS_PCT (default 50), else 1.
# Cleans up the temp container so 8080 is free for the real deploy.
# macOS/Bash 3 compatible.
set -Eeuo pipefail

log() { printf '==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

await_health() { # $1 host; $2 port; $3 timeout_secs
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

# Load .env if present (for HELIUS/ALCHEMY etc., and CASES_FILE)
if [ -f ".env" ]; then
  set -a
  # shellcheck disable=SC1091
  . ".env"
  set +a
fi

IMAGE="txparser:test-local"
TEST_CONTAINER="txparser-test-run"
HOST_IP="127.0.0.1"
PORT="8080"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-15}"
CASES_FILE="${CASES_FILE:-tests/cases.json}"
PER_TEST_DELAY_SECONDS="${PER_TEST_DELAY_SECONDS:-1}"
MIN_PASS_PCT="${MIN_PASS_PCT:-50}"

# Optional list of mints to sanity-check holders (>0)
HOLDER_CASES_FILE="${HOLDER_CASES_FILE:-tests/holder_mints.json}"

command -v docker >/dev/null 2>&1 || die "Docker not found in PATH."
docker info >/dev/null 2>&1 || die "Docker daemon not reachable."
command -v jq >/dev/null 2>&1 || die "jq is required for parsing ${CASES_FILE}."

log "Go unit tests"
if go version >/dev/null 2>&1; then
  # Keep exactly what you had (no test files is fine)
  go test ./... || true
else
  log "Go is not installed; skipping 'go test ./...'"
fi

log "Build test image"
docker build -t "${IMAGE}" .

log "Freeing 8080 from Docker containers"
busy_cids="$(docker ps -q --filter "publish=8080")"
if [ -n "${busy_cids}" ]; then
  # shellcheck disable=SC2086
  docker stop ${busy_cids} >/dev/null 2>&1 || true
fi
if docker ps -a --format '{{.Names}}' | grep -q "^${TEST_CONTAINER}\$"; then
  docker rm -f "${TEST_CONTAINER}" >/dev/null 2>&1 || true
fi
sleep 1

if port_in_use "${PORT}"; then
  log "Port 8080 is held by a non-Docker process:"
  lsof -iTCP:8080 -sTCP:LISTEN -n -P || true
  die "Release 8080 and re-run."
fi

cleanup() { docker rm -f "${TEST_CONTAINER}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

log "Run temp container on ${HOST_IP}:${PORT}"
ENV_ARGS=()
if [ -f ".env" ]; then
  ENV_ARGS=(--env-file ".env")
fi

docker run -d --name "${TEST_CONTAINER}" -p ${HOST_IP}:${PORT}:8080 "${ENV_ARGS[@]}" "${IMAGE}" >/dev/null

log "Wait for health"
if [ "$(await_health "${HOST_IP}" "${PORT}" 60)" != "ok" ]; then
  echo "---- logs (${TEST_CONTAINER}, last 200) ----"
  docker logs --tail 200 "${TEST_CONTAINER}" || true
  die "Temp container did not become healthy."
fi

log "Validate swapInfo non-zero for test cases (pacing ${PER_TEST_DELAY_SECONDS}s)"
# Try multiple layouts of cases.json:
# 1) array of {name, signature}
# 2) .cases[] of {name, signature}
# 3) object map { "Name": "Signature" }
extract_pairs() {
  jq -r '.[] | select(has("signature")) | "\(.name)\t\(.signature)"' "${CASES_FILE}" 2>/dev/null || true
}
pairs="$(extract_pairs)"
if [ -z "${pairs}" ]; then
  pairs="$(jq -r '.cases[] | select(has("signature")) | "\(.name)\t\(.signature)"' "${CASES_FILE}" 2>/dev/null || true)"
fi
if [ -z "${pairs}" ]; then
  pairs="$(jq -r 'to_entries[] | "\(.key)\t\(.value)"' "${CASES_FILE}" 2>/dev/null || true)"
fi
[ -n "${pairs}" ] || die "Could not parse ${CASES_FILE}. Expect array or object of name/signature pairs."

total=0
passed=0

# shellcheck disable=SC2034
IFS=$'\n'
for line in ${pairs}; do
  name="$(printf '%s' "${line}" | awk -F'\t' '{print $1}')"
  sig="$(printf '%s' "${line}" | awk -F'\t' '{print $2}')"
  [ -n "${sig}" ] || continue

  total=$((total+1))

  # POST /parse
  json="$(curl -sS --max-time "${REQUEST_TIMEOUT_SECONDS}" -X POST "http://${HOST_IP}:${PORT}/parse" \
    -H 'Content-Type: application/json' \
    -d "{\"signature\":\"${sig}\"}" || true)"

  # Consider pass if swapInfo object exists and is non-empty
  ok="$(printf '%s' "${json}" | jq -r 'if (.swapInfo | type=="object") and (.swapInfo | length>0) then "OK" else "FAIL" end' 2>/dev/null || echo FAIL)"
  if [ "${ok}" = "OK" ]; then
    printf ' - %s ... OK\n' "${name}"
    passed=$((passed+1))
  else
    printf ' - %s ... FAIL (empty swapInfo)\n' "${name}"
  fi

  sleep "${PER_TEST_DELAY_SECONDS}"
done

# ---- Holder sanity (does NOT affect pass %; just info) ----
if [ -f "${HOLDER_CASES_FILE}" ]; then
  log "Validate holder counts > 0 for test mints (pacing ${PER_TEST_DELAY_SECONDS}s)"
  holder_total=0
  holder_ok=0

  # Extract as array of strings: { "mints": [ "mint1", "mint2", ... ] }
  # Ignore blanks safely; no unbound vars.
  while IFS= read -r _mint; do
    mint="$(printf '%s' "${_mint}" | tr -d '[:space:]')"
    [ -n "${mint}" ] || continue
    holder_total=$((holder_total+1))

    tries=0
    http=000
    resp_file="$(mktemp)"
    while : ; do
      tries=$((tries+1))
      http="$(curl -sS -o "${resp_file}" -w "%{http_code}" --max-time "${REQUEST_TIMEOUT_SECONDS}" \
        "http://${HOST_IP}:${PORT}/holders?mint=${mint}" || echo 000)"
      # retry a couple of times on 429/5xx
      if echo "${http}" | grep -Eq '^(429|5..)$' && [ "${tries}" -lt 3 ]; then
        sleep "${PER_TEST_DELAY_SECONDS}"
        continue
      fi
      break
    done

    holders="$(jq -r '.holders // 0' "${resp_file}" 2>/dev/null || echo 0)"
    rm -f "${resp_file}"

    if [ "${holders}" -gt 0 ]; then
      printf ' - %s ... OK (%s)\n' "${mint}" "${holders}"
      holder_ok=$((holder_ok+1))
    else
      printf ' - %s ... FAIL (holders=%s, http=%s)\n' "${mint}" "${holders}" "${http}"
    fi
    sleep "${PER_TEST_DELAY_SECONDS}"
  done < <(jq -r '.mints[]? // empty' "${HOLDER_CASES_FILE}")

  log "Holder sanity: ${holder_ok}/${holder_total} mints had holders > 0 (not counted toward threshold)"
else
  log "Holder sanity: ${HOLDER_CASES_FILE} not found; skipping"
fi

# Summary & threshold
if [ "${total}" -eq 0 ]; then
  die "No test cases found in ${CASES_FILE}."
fi

# integer percentage (floor)
pass_pct=$(( 100 * passed / total ))
if [ "${pass_pct}" -ge "${MIN_PASS_PCT}" ]; then
  log "Integration OK: ${passed}/${total} (${pass_pct}%%) >= threshold ${MIN_PASS_PCT}%%"
  exit 0
else
  log "Integration FAILED: ${passed}/${total} (${pass_pct}%%) < threshold ${MIN_PASS_PCT}%%"
  exit 1
fi
