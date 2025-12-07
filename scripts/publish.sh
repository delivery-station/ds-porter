#!/bin/bash
set -e

VERSION=${1:-0.1.0}
REGISTRY=${2:-localhost:5000}
MANIFEST=${3:-ds.manifest.yaml}

# Ensure registry is running
./scripts/start-registry.sh

# Build porter locally to use it for push
echo "Building porter CLI..."
go build -o bin/ds-porter ./cmd/porter

# Build release artifacts (using make)
echo "Building release artifacts..."
make release-build-all VERSION=$VERSION

# Run push command
echo "Pushing artifacts for version $VERSION to $REGISTRY using manifest $MANIFEST..."
./bin/ds-porter push "$REGISTRY/delivery-station/porter:$VERSION" --manifest="$MANIFEST"

echo "Done!"
