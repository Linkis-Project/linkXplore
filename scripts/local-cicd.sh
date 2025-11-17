#!/usr/bin/env bash
set -Eeuo pipefail
trap 'ec=$?; echo "ERROR: $BASH_SOURCE:$LINENO (exit $ec)"; exit $ec' ERR

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Load .env if present (export while sourcing)
if [[ -f "${ROOT_DIR}/.env" ]]; then
  set -a
  . "${ROOT_DIR}/.env"
  set +a
fi

: "${VM_HOST:?VM_HOST missing}"
: "${VM_USER:?VM_USER missing}"
: "${LIVE_PORT:=8080}"
: "${STAGE_PORT:=8081}"
: "${DEPLOY_MODE:=simple}"
: "${REMOTE_ARCHIVE:=txparser-amd64.tar}"
: "${CASES_FILE:=tests/cases.json}"
: "${PER_TEST_DELAY_SECONDS:=1}"
: "${HOLDERS_FILE:=tests/holders.json}"
: "${PER_HOLDER_DELAY_SECONDS:=1}"

echo "==> Config"
echo "ROOT_DIR=${ROOT_DIR}"
echo "VM_HOST=${VM_HOST} VM_USER=${VM_USER}"
echo "LIVE_PORT=${LIVE_PORT} STAGE_PORT=${STAGE_PORT} MODE=${DEPLOY_MODE}"
echo "REMOTE_ARCHIVE=${REMOTE_ARCHIVE}"

echo "==> 1) run local tests"
"${ROOT_DIR}/scripts/run-tests.sh"

echo "==> 2) build & ship image tar"
TAG="$("${ROOT_DIR}/scripts/build-and-ship.sh")"
echo "TAG=${TAG}"

echo "==> 2.5) sync deploy scripts & test cases to VM"
ssh -q "${VM_USER}@${VM_HOST}" "mkdir -p ~/txparser/tests"

scp -q "${ROOT_DIR}/scripts/deploy-simple.sh" \
       "${ROOT_DIR}/scripts/deploy-bluegreen.sh" \
       "${VM_USER}@${VM_HOST}:/home/${VM_USER}/txparser/"

# swap cases
scp -q "${ROOT_DIR}/${CASES_FILE}" \
       "${VM_USER}@${VM_HOST}:/home/${VM_USER}/txparser/tests/cases.json"

# holders cases (optional)
if [[ -f "${ROOT_DIR}/${HOLDERS_FILE}" ]]; then
  scp -q "${ROOT_DIR}/${HOLDERS_FILE}" \
         "${VM_USER}@${VM_HOST}:/home/${VM_USER}/txparser/tests/holders.json"
fi

# sync .env so the container gets SOLANA_RPC_URL / *_FOR_COUNTER on the VM
if [[ -f "${ROOT_DIR}/.env" ]]; then
  scp -q "${ROOT_DIR}/.env" \
    "${VM_USER}@${VM_HOST}:/home/${VM_USER}/txparser/.env"
fi

ssh -q "${VM_USER}@${VM_HOST}" "chmod +x ~/txparser/deploy-simple.sh ~/txparser/deploy-bluegreen.sh"

echo "==> 3) preflight docker on VM"
ssh "${VM_USER}@${VM_HOST}" "bash -s" <<'EOF'
set -e
if ! sudo systemctl is-active --quiet docker 2>/dev/null; then
  sudo systemctl start docker 2>/dev/null || sudo service docker start 2>/dev/null
fi
sudo systemctl enable docker 2>/dev/null || true
sudo docker info >/dev/null
EOF

echo "==> 3b) deploy on VM (${DEPLOY_MODE})"
if [[ "${DEPLOY_MODE}" == "bluegreen" ]]; then
  ssh "${VM_USER}@${VM_HOST}" "bash -s" <<EOF
set -e
export PER_TEST_DELAY_SECONDS="${PER_TEST_DELAY_SECONDS}"
export PER_HOLDER_DELAY_SECONDS="${PER_HOLDER_DELAY_SECONDS}"
sudo bash ~/txparser/deploy-bluegreen.sh "/home/${VM_USER}/${REMOTE_ARCHIVE}" "${LIVE_PORT}" "${STAGE_PORT}"
EOF
else
  ssh "${VM_USER}@${VM_HOST}" "bash -s" <<EOF
set -e
sudo bash ~/txparser/deploy-simple.sh "/home/${VM_USER}/${REMOTE_ARCHIVE}" "${LIVE_PORT}"
EOF
fi

echo "==> 4) remote smoke"
"${ROOT_DIR}/scripts/smoke-remote.sh" "${VM_HOST}" "${LIVE_PORT}" "${ROOT_DIR}/tests/cases.json" 15

echo "==> Done. Deployed tag ${TAG}"
