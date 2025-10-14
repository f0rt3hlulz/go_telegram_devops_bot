#!/usr/bin/env bash
set -euo pipefail

IMAGE_NAME="devops-telegram-bot"
CONTAINER_NAME="devops-telegram-bot"

WORKDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$WORKDIR"

echo "[deploy] Building Docker image $IMAGE_NAME..."
docker build -t "$IMAGE_NAME" .

if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
  echo "[deploy] Removing existing container $CONTAINER_NAME..."
  docker rm -f "$CONTAINER_NAME"
fi

echo "[deploy] Starting container $CONTAINER_NAME..."
docker run -d \
  --name "$CONTAINER_NAME" \
  --env-file .env \
  -p 8080:8080 \
  --restart unless-stopped \
  "$IMAGE_NAME"

echo "[deploy] Done. Use 'docker logs -f $CONTAINER_NAME' to tail logs."
