#!/usr/bin/env bash
set -euo pipefail

ARCHIVE="$1"  # /home/azureuser/txparser-amd64.tar
LIVE_PORT="${2:-8080}"
STAGE_PORT="${3:-8081}"
PER_TEST_DELAY_SECONDS="${PER_TEST_DELAY_SECONDS:-1}"
PER_HOLDER_DELAY_SECONDS="${PER_HOLDER_DELAY_SECONDS:-1}"

# ensure docker daemon is up
if ! systemctl is-active --quiet docker 2>/dev/null; then
  systemctl start docker 2>/dev/null || service docker start 2>/dev/null || true
fi

echo "==> docker load"
docker load -i "$ARCHIVE" >/dev/null

REF=$(docker image ls --format '{{.Repository}}:{{.Tag}}' | grep -E '^txparser-local:' | head -n1)
if [[ -z "${REF}" ]]; then
  echo "ERROR: cannot find loaded txparser-local image"
  exit 1
fi
echo "Using image: $REF"

NEW=txparser_new
OLD=txparser

# optional env-file on VM
ENV_FILE="$HOME/txparser/.env"
ENV_ARGS=()
if [[ -f "$ENV_FILE" ]]; then
  ENV_ARGS=(--env-file "$ENV_FILE")
fi

echo "==> Start new on ${STAGE_PORT}"
docker rm -f "${NEW}" >/dev/null 2>&1 || true
docker run -d --name "${NEW}" \
  -p 0.0.0.0:${STAGE_PORT}:8080 \
  --restart unless-stopped \
  "${ENV_ARGS[@]}" \
  "$REF"

echo "==> Health (staging)"
for i in {1..40}; do
  curl -sf "http://127.0.0.1:${STAGE_PORT}/healthz" >/dev/null && break
  sleep 0.25
done

echo "==> Smoke (staging)"
CASES=~/txparser/tests/cases.json
HOLDERS=~/txparser/tests/holders.json
if [[ ! -f "$CASES" ]]; then
  echo "cases.json not found at $CASES"
  exit 1
fi

fail=0
# ---- swap checks ----
while IFS= read -r row; do
  sig=$(echo "$row" | jq -r '.signature')
  ok=0
  for attempt in 1 2; do
    resp=$(curl -sS --max-time 15 -X POST "http://127.0.0.1:${STAGE_PORT}/parse" \
      -H 'Content-Type: application/json' -d "{\"signature\":\"$sig\"}") || true
    if echo "$resp" | jq -e ' has("transaction") and has("swapInfo") and .swapInfo != null and ( (.swapInfo.TokenInAmount // 0) > 0 or (.swapInfo.TokenOutAmount // 0) > 0 or ((.swapInfo.AMMs | length) // 0) > 0 ) ' >/dev/null; then
      ok=1; break
    fi
    sleep 1
  done
  [[ "$ok" -eq 1 ]] || fail=1
  sleep "${PER_TEST_DELAY_SECONDS}"
done < <(jq -c '.[]' "$CASES")

# ---- holders checks (optional) ----
if [[ -f "$HOLDERS" ]]; then
  echo "==> Holder smoke (staging)"
  while IFS= read -r row; do
    mint=$(echo "$row" | jq -r '.mint // empty')
    [[ -n "$mint" ]] || continue
    ok=0
    for attempt in 1 2; do
      hresp=$(curl -sS --max-time 15 "http://127.0.0.1:${STAGE_PORT}/holders?mint=${mint}") || true
      if echo "$hresp" | jq -e '((.holders // -1) | tonumber) > 0' >/dev/null; then
        ok=1; break
      fi
      sleep 1
    done
    [[ "$ok" -eq 1 ]] || fail=1
    sleep "${PER_HOLDER_DELAY_SECONDS}"
  done < <(jq -c '.[]' "$HOLDERS")
else
  echo "No holders.json at $HOLDERS; skipping holder smoke."
fi

if [[ "$fail" -ne 0 ]]; then
  echo "Staging smoke FAILED. Rolling back."
  docker rm -f "${NEW}" || true
  exit 1
fi

echo "==> Promote: replace old"
docker rm -f "${OLD}" >/dev/null 2>&1 || true
docker rename "${NEW}" "${OLD}"

# rebind live port
docker stop "${OLD}" >/dev/null 2>&1 || true
docker rm "${OLD}" >/dev/null 2>&1 || true
docker run -d --name "${OLD}" \
  -p 0.0.0.0:${LIVE_PORT}:8080 \
  --restart unless-stopped \
  "${ENV_ARGS[@]}" \
  "$REF"

echo "==> Health (live)"
for i in {1..40}; do
  curl -sf "http://127.0.0.1:${LIVE_PORT}/healthz" >/dev/null && break
  sleep 0.25
done
echo "OK"
