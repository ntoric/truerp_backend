#!/usr/bin/env bash
# Pull the TruERP API image and recreate the container with zero local rebuild.
# Used by GitHub Actions CD and safe to run manually on the server.
#
# Usage (from the directory that contains prod-compose.yml):
#   IMAGE_TAG=1.2.3 ./scripts/deploy.sh
#   IMAGE_TAG=latest ./scripts/deploy.sh
#
# Optional env:
#   COMPOSE_FILE   (default: prod-compose.yml)
#   DOCKERHUB_USERNAME
#   HEALTH_URL     (default: http://127.0.0.1:${API_PUBLISH_PORT:-8088}/health)
#   HEALTH_RETRIES (default: 12)
#   HEALTH_SLEEP   (default: 5)

set -euo pipefail

COMPOSE_FILE="${COMPOSE_FILE:-prod-compose.yml}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
API_PUBLISH_PORT="${API_PUBLISH_PORT:-8088}"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:${API_PUBLISH_PORT}/health}"
HEALTH_RETRIES="${HEALTH_RETRIES:-12}"
HEALTH_SLEEP="${HEALTH_SLEEP:-5}"

if [[ ! -f "${COMPOSE_FILE}" ]]; then
  echo "error: ${COMPOSE_FILE} not found in $(pwd)" >&2
  exit 1
fi

export IMAGE_TAG
if [[ -n "${DOCKERHUB_USERNAME:-}" ]]; then
  export DOCKERHUB_USERNAME
fi

echo "==> Deploying truerp-api:${IMAGE_TAG}"
docker compose -f "${COMPOSE_FILE}" pull api
docker compose -f "${COMPOSE_FILE}" up -d --remove-orphans --force-recreate api

echo "==> Waiting for health: ${HEALTH_URL}"
ok=0
for ((i = 1; i <= HEALTH_RETRIES; i++)); do
  if curl -fsS "${HEALTH_URL}" >/dev/null 2>&1; then
    ok=1
    break
  fi
  echo "    attempt ${i}/${HEALTH_RETRIES} — not ready yet"
  sleep "${HEALTH_SLEEP}"
done

if [[ "${ok}" -ne 1 ]]; then
  echo "error: health check failed after deploy" >&2
  docker compose -f "${COMPOSE_FILE}" ps
  docker compose -f "${COMPOSE_FILE}" logs --tail=80 api || true
  exit 1
fi

echo "==> Healthy. Cleaning unused images..."
docker image prune -f >/dev/null || true
echo "==> Deploy complete (truerp-api:${IMAGE_TAG})"
