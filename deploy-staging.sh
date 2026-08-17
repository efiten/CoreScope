#!/bin/bash
set -e

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$DEPLOY_DIR"

echo "[staging] Fetching latest from origin..."
git fetch --tags origin

BRANCH="${1:-master}"
echo "[staging] Checking out $BRANCH..."
git reset --hard "origin/$BRANCH"

# Staging runs its own config when config.staging.json is present, so retention
# and memory knobs can be trialled here without touching live (both used to
# mount the same config.json). Falls back to the live config when absent.
CONFIG_FILE="$DEPLOY_DIR/config.json"
if [ -f "$DEPLOY_DIR/config.staging.json" ]; then
  CONFIG_FILE="$DEPLOY_DIR/config.staging.json"
fi
echo "[staging] Config: $CONFIG_FILE"

GIT_COMMIT=$(git rev-parse --short HEAD)
APP_VERSION=$(git tag --points-at HEAD | grep -E '^v[0-9]' | sort -V | tail -1)
APP_VERSION=${APP_VERSION:-$(git describe --tags --abbrev=0 2>/dev/null || echo dev)}
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo "[staging] Version: ${APP_VERSION} commit: ${GIT_COMMIT}"
echo "[staging] Building Docker image (linux/arm64)..."
docker build \
  --build-arg BUILDPLATFORM=linux/arm64 \
  --build-arg TARGETOS=linux \
  --build-arg TARGETARCH=arm64 \
  --build-arg APP_VERSION="${APP_VERSION}" \
  --build-arg GIT_COMMIT="${GIT_COMMIT}" \
  --build-arg BUILD_TIME="${BUILD_TIME}" \
  -t meshcore-analyzer-staging .

echo "[staging] Stopping old container (30s grace period)..."
docker stop -t 30 meshcore-staging 2>/dev/null || true
docker rm meshcore-staging 2>/dev/null || true

echo "[staging] Starting new container..."
docker run -d --name meshcore-staging \
  --restart unless-stopped \
  --network mesh-internal \
  -p 3001:3000 \
  -v "$CONFIG_FILE:/app/config.json:ro" \
  -v meshcore-staging-data:/app/data \
  meshcore-analyzer-staging

echo "[staging] Done. Live at https://staging.on8ar.eu"
