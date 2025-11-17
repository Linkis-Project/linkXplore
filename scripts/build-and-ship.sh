#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${ROOT_DIR}/.env" 2>/dev/null || true

: "${VM_HOST:?VM_HOST missing}"
: "${VM_USER:?VM_USER missing}"
: "${IMAGE_NAME:=txparser-local}"
: "${REMOTE_ARCHIVE:=txparser-amd64.tar}"

TAG=$(date +%Y%m%d-%H%M%S)

# Build linux/amd64 image (quiet logs)
docker buildx create --use >/dev/null 2>&1 || true
docker buildx build --platform linux/amd64 \
  -t "${IMAGE_NAME}:${TAG}" --load "${ROOT_DIR}" >/dev/null

# Save & copy the tar (quiet logs)
docker save "${IMAGE_NAME}:${TAG}" -o "${ROOT_DIR}/${REMOTE_ARCHIVE}"
scp -q "${ROOT_DIR}/${REMOTE_ARCHIVE}" "${VM_USER}@${VM_HOST}:/home/${VM_USER}/${REMOTE_ARCHIVE}"

# IMPORTANT: print only the tag
echo -n "${TAG}"
