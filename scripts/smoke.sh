#!/usr/bin/env bash
set -euo pipefail

HOST="${1:-127.0.0.1}"
PORT="${2:-8080}"

echo "==> health"
curl -sf "http://${HOST}:${PORT}/healthz" >/dev/null

echo "==> check cases.json on remote endpoint"
fail=0
while IFS= read -r row; do
  sig=$(echo "$row" | jq -r '.signature')
  label=$(echo "$row" | jq -r '.label')
  printf "  - %s ... " "$label"

  resp=$(curl -sS --max-time 15 -X POST "http://${HOST}:${PORT}/parse" \
    -H 'Content-Type: application/json' \
    -d "{\"signature\":\"$sig\"}")

  if echo "$resp" | jq -e '
      has("transaction") and has("swapInfo") and
      .swapInfo != null and (
        (.swapInfo.TokenInAmount // 0) > 0 or
        (.swapInfo.TokenOutAmount // 0) > 0 or
        ((.swapInfo.AMMs | length) // 0) > 0
      )
    ' >/dev/null; then
    echo "OK"
  else
    echo "FAIL"
    fail=1
  fi
done < <(jq -c '.[]' tests/cases.json)

if [ "$fail" -ne 0 ]; then
  echo "smoke FAILED"
  exit 1
fi
echo "smoke OK"
