#!/bin/bash
set -e

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$DEPLOY_DIR"

echo "[deploy] Fetching latest from origin..."
git fetch --tags origin

echo "[deploy] Resetting to origin/master..."
git reset --hard origin/master

GIT_COMMIT=$(git rev-parse --short HEAD)
APP_VERSION=$(git tag --points-at HEAD | grep -E '^v[0-9]' | sort -V | tail -1)
APP_VERSION=${APP_VERSION:-$(git describe --tags --abbrev=0 2>/dev/null || echo dev)}
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo "[deploy] Version: ${APP_VERSION} commit: ${GIT_COMMIT}"
echo "[deploy] Building Docker image (linux/arm64)..."
docker build \
  --build-arg BUILDPLATFORM=linux/arm64 \
  --build-arg TARGETOS=linux \
  --build-arg TARGETARCH=arm64 \
  --build-arg APP_VERSION="${APP_VERSION}" \
  --build-arg GIT_COMMIT="${GIT_COMMIT}" \
  --build-arg BUILD_TIME="${BUILD_TIME}" \
  -t meshcore-analyzer .

echo "[deploy] Stopping old container (30s grace period)..."
docker stop -t 30 meshcore-analyzer && docker rm meshcore-analyzer
docker run -d --name meshcore-analyzer \
  --restart unless-stopped \
  --network mesh-internal \
  -p 3000:3000 \
  -v "$(pwd)/config.json:/app/config.json:ro" \
  -v meshcore-data:/app/data \
  meshcore-analyzer

echo "[deploy] Done. Live at https://analyzer.on8ar.eu"
