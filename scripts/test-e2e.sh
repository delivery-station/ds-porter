#!/usr/bin/env bash
set -euo pipefail

# Local e2e test runner for ds-porter
# Supports both docker and podman
# Usage: ./scripts/test-e2e.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
REGISTRY_NAME="ds-porter-registry"
REGISTRY_PORT=5555
REGISTRY_HOST="localhost:${REGISTRY_PORT}"

# Detect container runtime
if command -v podman &> /dev/null; then
    RUNTIME="podman"
elif command -v docker &> /dev/null; then
    RUNTIME="docker"
else
    echo "ERROR: Neither podman nor docker found. Install one to run e2e tests."
    exit 1
fi

echo "=== ds-porter E2E Test Runner ==="
echo "Runtime: ${RUNTIME}"
echo "Registry: ${REGISTRY_HOST}"
echo ""

# Cleanup function
cleanup() {
    echo ""
    echo "Cleaning up..."
    ${RUNTIME} stop "${REGISTRY_NAME}" 2>/dev/null || true
    ${RUNTIME} rm "${REGISTRY_NAME}" 2>/dev/null || true
}
trap cleanup EXIT

# Stop existing registry if running
echo "Checking for existing registry..."
if ${RUNTIME} ps --format '{{.Names}}' | grep -q "^${REGISTRY_NAME}$"; then
    echo "Registry '${REGISTRY_NAME}' is already running."
    echo "Stopping and restarting..."
    ${RUNTIME} stop "${REGISTRY_NAME}"
    ${RUNTIME} rm "${REGISTRY_NAME}"
fi

# Start registry
echo "Starting OCI registry on ${REGISTRY_HOST}..."
${RUNTIME} run -d \
    --name "${REGISTRY_NAME}" \
    -p "${REGISTRY_PORT}:5000" \
    registry:2

# Wait for registry to be ready
echo "Waiting for registry to be ready..."
for i in $(seq 1 30); do
    if nc -z localhost "${REGISTRY_PORT}" 2>/dev/null; then
        echo "Registry is ready!"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "ERROR: Registry failed to start within 30 seconds"
        exit 1
    fi
    sleep 1
done

echo ""
echo "=== Running E2E Tests ==="
cd "${PROJECT_DIR}"
go test -v -tags=e2e -timeout 5m ./test/e2e/...

echo ""
echo "=== E2E Tests Complete ==="
echo "Registry container '${REGISTRY_NAME}' will be cleaned up automatically."
