#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${ROOT_DIR}/.env" 2>/dev/null || true

HOST="${1:?host}"
PORT="${2:-8080}"
CASES_FILE="${3:-tests/cases.json}"
TIMEOUT="${4:-15}"
: "${PER_TEST_DELAY_SECONDS:=1}"

# Optional holders file (new)
HOLDERS_FILE="${HOLDERS_FILE:-tests/holders.json}"
: "${PER_HOLDER_DELAY_SECONDS:=1}"

echo "==> Remote health"
curl -sf "http://${HOST}:${PORT}/healthz" >/dev/null

# ---------- Swaps ----------
echo "==> Remote swap cases (pacing ${PER_TEST_DELAY_SECONDS}s)"
fail=0
while IFS= read -r row; do
  sig=$(echo "$row" | jq -r '.signature')
  label=$(echo "$row" | jq -r '.label')
  printf "  - %s ... " "$label"

  sleep "${PER_TEST_DELAY_SECONDS}"

  tries=0; max_tries=3
  while : ; do
    tries=$((tries+1))
    http=$(curl -sS -o /tmp/remote_resp.json -w "%{http_code}" --max-time "${TIMEOUT}" \
      -X POST "http://${HOST}:${PORT}/parse" \
      -H 'Content-Type: application/json' \
      -d "{\"signature\":\"$sig\"}") || http=000

    if [[ "$http" =~ ^(429|5..)$ ]] && [[ $tries -lt $max_tries ]]; then
      sleep "${PER_TEST_DELAY_SECONDS}"
      continue
    fi
    break
  done

  resp="$(cat /tmp/remote_resp.json 2>/dev/null || echo '{}')"
  if echo "$resp" | jq -e 'has("transaction") and .swapInfo != null' >/dev/null; then
    echo "OK"
  else
    echo "FAIL"
    fail=1
  fi
done < <(jq -c '.[]' "${CASES_FILE}")

# ---------- Holders (optional) ----------
if [[ -f "${HOLDERS_FILE}" ]]; then
  echo "==> Remote holder cases (pacing ${PER_HOLDER_DELAY_SECONDS}s)"
  while IFS= read -r row; do
    mint=$(echo "$row" | jq -r '.mint // empty')
    label=$(echo "$row" | jq -r '.name // .label // empty')
    [[ -n "$mint" ]] || continue
    printf "  - %s ... " "${label:-$mint}"

    sleep "${PER_HOLDER_DELAY_SECONDS}"

    tries=0; max_tries=3; http=000
    while : ; do
      tries=$((tries+1))
      http=$(curl -sS -o /tmp/remote_hresp.json -w "%{http_code}" --max-time "${TIMEOUT}" \
        "http://${HOST}:${PORT}/holders?mint=${mint}" ) || http=000
      if [[ "$http" =~ ^(429|5..)$ ]] && [[ $tries -lt $max_tries ]]; then
        sleep "${PER_HOLDER_DELAY_SECONDS}"
        continue
      fi
      break
    done

    hresp="$(cat /tmp/remote_hresp.json 2>/dev/null || echo '{}')"
    if echo "$hresp" | jq -e '((.holders // -1) | tonumber) > 0' >/dev/null; then
      echo "OK"
    else
      echo "FAIL"
      fail=1
    fi
  done < <(jq -c '.[]' "${HOLDERS_FILE}")
else
  echo "==> No holders file at ${HOLDERS_FILE}; skipping holder smoke."
fi

[ "$fail" -eq 0 ] && echo "smoke OK" || { echo "smoke FAILED"; exit 1; }
