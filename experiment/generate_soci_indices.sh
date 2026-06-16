#!/bin/bash
set -euo pipefail

# Run this on the Linux test host after push_images.sh has pushed images to ECR.
# Requires: containerd running, custom soci binary at out/soci

ECR_REPO="299170649678.dkr.ecr.us-west-2.amazonaws.com/lod-testing"
REGION="us-west-2"
SOCI_BIN="/home/ssm-user/soci-snapshotter/out/soci"

TAGS=(
  "redis-7"
  "nginx-1.27"
  "postgres-16"
  "python-3.12"
  "node-22"
  "golang-1.23"
  "cuda-12.4.0-runtime"
  "rust-1.79"
  "pytorch-2.4.0"
)

ECR_TOKEN=$(aws ecr get-login-password --region "$REGION")

for TAG in "${TAGS[@]}"; do
  IMAGE="$ECR_REPO:$TAG"
  echo "=== [$TAG] ==="

  echo "  Pulling into containerd..."
  ctr image pull --user "AWS:$ECR_TOKEN" --platform linux/amd64 "$IMAGE"

  echo "  Creating SOCI index..."
  $SOCI_BIN create --platform linux/amd64 "$IMAGE"

  echo "  Pushing SOCI index..."
  $SOCI_BIN push --user "AWS:$ECR_TOKEN" --platform linux/amd64 "$IMAGE"

  echo "  Done."
  echo ""
done

echo "=== All SOCI indices generated and pushed ==="
