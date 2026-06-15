# LOD Oracle Experiment — Test Images

## Buckets

| Bucket | Uncompressed Size | Purpose |
|--------|-------------------|---------|
| Small  | < 100 MB          | Control — lazy loading has minimal benefit, verify low stall rate |
| Medium | 100–500 MB        | Moderate LOD candidate — subset needed at startup |
| Large  | 1 GB+             | Primary LOD candidate — large image, small startup footprint |

## Images

### Small (< 100 MB uncompressed)

| Image | Tag | ~Size | Startup Command | Rationale |
|-------|-----|-------|-----------------|-----------|
| `public.ecr.aws/docker/library/redis` | `7` | ~80 MB | `redis-server --daemonize no` | Fixed startup file access |
| `public.ecr.aws/docker/library/nginx` | `1.27` | ~60 MB | `nginx -g "daemon off;"` | Minimal, predictable init |
| `public.ecr.aws/docker/library/postgres` | `16` | ~100 MB | `docker-entrypoint.sh postgres` | Deterministic initdb |

### Medium (100–500 MB uncompressed)

| Image | Tag | ~Size | Startup Command | Rationale |
|-------|-----|-------|-----------------|-----------|
| `public.ecr.aws/docker/library/python` | `3.12` | ~360 MB | `python -c "import sys; print(sys.version)"` | Touches runtime subset |
| `public.ecr.aws/docker/library/node` | `22` | ~350 MB | `node -e "process.exit(0)"` | Touches V8 + core modules only |
| `public.ecr.aws/docker/library/golang` | `1.23` | ~290 MB | `go version` | Touches toolchain subset |

### Large (1 GB+ uncompressed)

| Image | Tag | ~Size | Startup Command | Rationale |
|-------|-----|-------|-----------------|-----------|
| `public.ecr.aws/nvidia/cuda` | `12.4.0-runtime-ubuntu22.04` | ~1.5 GB | `nvidia-smi || true` | CUDA libs, predictable access |
| `public.ecr.aws/docker/library/rust` | `1.79` | ~1.2 GB | `rustc --version` | Large toolchain, small startup footprint |
| `public.ecr.aws/pytorch/pytorch` | `2.3.0-gpu-py311-cu121-ubuntu20.04` | ~5 GB | `python -c "import torch; print(torch.__version__)"` | Huge image, exercises fraction at startup |

## Experiment execution

Each image is pulled with a cold cache. The startup command runs until completion or a 30s timeout.

```
ctr image rpull <ecr_image_ref>
ctr run --rm <ecr_image_ref> test-<name> <startup_command>
```

## ECR setup

- Region: `us-west-2`
- Repository: `299170649678.dkr.ecr.us-west-2.amazonaws.com/lod-testing`
- Each image tagged as: `lod-testing:<original_name>-<tag>` (e.g. `lod-testing:redis-7`)

### Prep per image

```bash
ECR_REPO=299170649678.dkr.ecr.us-west-2.amazonaws.com/lod-testing

# Push image to ECR (via docker since ctr push needs credentials setup)
docker pull <source_image>:<tag>
docker tag <source_image>:<tag> $ECR_REPO:<name>-<tag>
docker push $ECR_REPO:<name>-<tag>

# Pull into containerd's content store (needed for soci create)
ctr image pull $ECR_REPO:<name>-<tag>

# Create SOCI index from local content store
soci create $ECR_REPO:<name>-<tag>

# Push SOCI index to ECR (references the already-pushed image)
soci push $ECR_REPO:<name>-<tag>
```
