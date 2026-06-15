#!/bin/bash
set -euo pipefail

ECR_REPO="299170649678.dkr.ecr.us-west-2.amazonaws.com/lod-testing"
REGION="us-west-2"

IMAGES=(
  "public.ecr.aws/docker/library/redis:7|redis-7"
  "public.ecr.aws/docker/library/nginx:1.27|nginx-1.27"
  "public.ecr.aws/docker/library/postgres:16|postgres-16"
  "public.ecr.aws/docker/library/python:3.12|python-3.12"
  "public.ecr.aws/docker/library/node:22|node-22"
  "public.ecr.aws/docker/library/golang:1.23|golang-1.23"
  "docker.io/nvidia/cuda:12.4.0-runtime-ubuntu22.04|cuda-12.4.0-runtime"
  "public.ecr.aws/docker/library/rust:1.79|rust-1.79"
  "docker.io/pytorch/pytorch:2.4.0-cuda12.4-cudnn9-runtime|pytorch-2.4.0"
)

# Authenticate with private ECR
aws ecr get-login-password --region "$REGION" | finch login --username AWS --password-stdin "$ECR_REPO"

process_image() {
  local entry="$1"
  local SOURCE="${entry%%|*}"
  local TAG="${entry##*|}"
  local TARGET="$ECR_REPO:$TAG"

  echo "[$TAG] Pulling $SOURCE..."
  finch pull "$SOURCE"

  echo "[$TAG] Tagging -> $TARGET"
  finch tag "$SOURCE" "$TARGET"

  # Push image + generate and push SOCI index in one step
  echo "[$TAG] Pushing image + SOCI index..."
  finch push --snapshotter soci "$TARGET"

  echo "[$TAG] Done."
}

echo "=== Pushing images + SOCI indices to ECR ==="
for entry in "${IMAGES[@]}"; do
  process_image "$entry" &
done
wait
echo "=== All images and SOCI indices pushed ==="
