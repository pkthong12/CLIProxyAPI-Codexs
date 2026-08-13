#!/usr/bin/env bash

set -euo pipefail

IMAGE="${CODEXS_IMAGE:?Set CODEXS_IMAGE to the candidate image tag or digest.}"
CONFIG_FILE="${CANARY_CONFIG_FILE:?Set CANARY_CONFIG_FILE to a config.yaml path.}"
AUTH_DIRECTORY="${CANARY_AUTH_DIRECTORY:?Set CANARY_AUTH_DIRECTORY to the existing auth directory.}"
DATA_DIRECTORY="${CANARY_DATA_DIRECTORY:?Set CANARY_DATA_DIRECTORY to an empty writable canary data directory.}"
API_KEY_FILE="${CANARY_API_KEY_FILE:?Set CANARY_API_KEY_FILE to a local client API-key file.}"
EXPECTED_MODEL="${EXPECTED_MODEL:-}"
CANARY_PORT="${CANARY_PORT:-18318}"
CANARY_NAME="${CANARY_NAME:-cli-proxy-codexs-canary}"
STARTUP_ATTEMPTS="${STARTUP_ATTEMPTS:-45}"

if [[ ! -f "$CONFIG_FILE" || ! -f "$API_KEY_FILE" || ! -d "$AUTH_DIRECTORY" ]]; then
  printf '%s\n' 'Canary config, API-key file, or auth directory is unavailable.' >&2
  exit 1
fi

mkdir -p "$DATA_DIRECTORY"
API_KEY="$(tr -d '\r\n' < "$API_KEY_FILE")"

cleanup() {
  docker rm -f "$CANARY_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d \
  --name "$CANARY_NAME" \
  --restart no \
  -p "127.0.0.1:${CANARY_PORT}:8317" \
  -v "${CONFIG_FILE}:/CLIProxyAPI/config.yaml:ro" \
  -v "${AUTH_DIRECTORY}:/root/.cli-proxy-api:ro" \
  -v "${DATA_DIRECTORY}:/app/data" \
  "$IMAGE" \
  ./CLIProxyAPI --config /CLIProxyAPI/config.yaml >/dev/null

for ((attempt = 1; attempt <= STARTUP_ATTEMPTS; attempt++)); do
  if curl --fail --silent --show-error "http://127.0.0.1:${CANARY_PORT}/v1/models" >/dev/null; then
    break
  fi
  if (( attempt == STARTUP_ATTEMPTS )); then
    docker logs "$CANARY_NAME" >&2 || true
    printf '%s\n' 'Canary did not become ready.' >&2
    exit 1
  fi
  sleep 1
done

MODELS_RESPONSE="$(curl --fail --silent --show-error \
  -H "Authorization: Bearer ${API_KEY}" \
  "http://127.0.0.1:${CANARY_PORT}/v1/models")"

printf '%s\n' "$MODELS_RESPONSE" | jq -e '.data | type == "array"' >/dev/null

if [[ -n "$EXPECTED_MODEL" ]]; then
  printf '%s\n' "$MODELS_RESPONSE" | jq -e --arg model "$EXPECTED_MODEL" \
    '.data | any(.id == $model)' >/dev/null
fi

printf 'Canary passed on 127.0.0.1:%s using image %s\n' "$CANARY_PORT" "$IMAGE"
