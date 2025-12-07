#!/bin/bash
set -e

# Start registry
echo "Starting local registry..."
docker-compose up -d

echo "Waiting for registry to be ready..."
# Wait for registry to be ready
for i in {1..30}; do
    if curl -s http://localhost:5000/v2/ > /dev/null; then
        echo "Registry is ready!"
        break
    fi
    sleep 1
done

echo "Registry UI available at http://localhost:8080"
