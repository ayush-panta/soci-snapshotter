#!/bin/bash
set -euo pipefail

###############################################################################
# LOD Oracle Experiment Runner
#
# Usage:
#   sudo ./run_experiment.sh
#
# Prerequisites:
#   - Custom soci-snapshotter built at /home/ssm-user/soci-snapshotter/out/soci-snapshotter-grpc
#   - containerd running
#   - ECR images already pushed with SOCI indices
#   - metrics_address = "localhost:1338" in config
###############################################################################

ECR_REPO="299170649678.dkr.ecr.us-west-2.amazonaws.com/lod-testing"
REGION="us-west-2"
SNAPSHOTTER_BIN="/home/ssm-user/soci-snapshotter/out/soci-snapshotter-grpc"
CONFIG="/etc/soci-snapshotter-grpc/config.toml"
ACCESS_LOG="/tmp/soci_access.log"
RESULTS="/home/ssm-user/soci-snapshotter/experiment/results.csv"
METRICS_ADDR="localhost:1338"
TRIALS=10

# Image definitions: tag|startup_command
IMAGES=(
  "redis-7|redis-server --daemonize no --save '' --appendonly no"
  "nginx-1.27|nginx -g 'daemon off;'"
  "postgres-16|postgres --version"
  "python-3.12|python -c 'import sys; print(sys.version)'"
  "node-22|node -e 'process.exit(0)'"
  "golang-1.23|go version"
  "cuda-12.4.0-runtime|cat /usr/local/cuda/version.json"
  "rust-1.79|rustc --version"
  "pytorch-2.4.0|python -c 'import torch; print(torch.__version__)'"
)

###############################################################################
# Helper functions
###############################################################################

log() {
  echo "[$(date '+%H:%M:%S')] $*"
}

stop_snapshotter() {
  pkill -f soci-snapshotter-grpc 2>/dev/null || true
  sleep 2
}

start_snapshotter() {
  local mode="$1"  # "baseline", "oracle", or "control"

  local env_vars=""
  case "$mode" in
    baseline)
      env_vars="SOCI_ACCESS_LOG=$ACCESS_LOG"
      ;;
    oracle)
      env_vars="SOCI_ORACLE_LOG=$ACCESS_LOG"
      ;;
    control)
      env_vars=""
      ;;
  esac

  env $env_vars $SNAPSHOTTER_BIN --config "$CONFIG" &
  sleep 3  # wait for snapshotter to be ready
}

clear_cache() {
  # Soci snapshotter cache
  rm -rf /var/lib/soci-snapshotter-grpc/ 2>/dev/null || true
  mkdir -p /var/lib/soci-snapshotter-grpc

  # Containerd snapshotter state
  rm -rf /var/lib/containerd/io.containerd.snapshotter.v1.soci/ 2>/dev/null || true

  # Containerd content store (cached blobs/manifests)
  rm -rf /var/lib/containerd/io.containerd.content.v1.content/ 2>/dev/null || true

  # Kernel page cache
  sync && echo 3 > /proc/sys/vm/drop_caches 2>/dev/null || true
}

remove_image() {
  local image="$1"
  ctr image rm "$image" 2>/dev/null || true
}

get_stall_count() {
  curl -s "$METRICS_ADDR/metrics" 2>/dev/null | \
    grep 'soci_fs_operation_count.*synchronous_read_remote_registry_fetch_count' | \
    awk '{sum += $2} END {print sum+0}'
}

run_trial() {
  local tag="$1"
  local cmd="$2"
  local mode="$3"
  local trial="$4"
  local image="$ECR_REPO:$tag"

  log "  [$mode] Trial $trial: $tag"

  # Clean slate
  stop_snapshotter
  clear_cache
  remove_image "$image"

  # Start snapshotter in desired mode
  start_snapshotter "$mode"

  # Pull and run
  local time_start=$(date +%s%N)

  ctr image rpull --user "AWS:$ECR_TOKEN" --snapshotter soci "$image" > /dev/null 2>&1

  # Run startup command with 30s timeout
  timeout 30 ctr run --rm --snapshotter soci "$image" "test-$tag-$trial" \
    sh -c "$cmd" > /dev/null 2>&1 || true

  local time_end=$(date +%s%N)
  local elapsed_ms=$(( (time_end - time_start) / 1000000 ))

  # Collect stall metric
  local stalls
  stalls=$(get_stall_count)

  # Write result
  echo "$tag,$mode,$trial,$stalls,$elapsed_ms" >> "$RESULTS"

  log "  [$mode] Trial $trial: stalls=$stalls elapsed=${elapsed_ms}ms"
}

###############################################################################
# Main
###############################################################################

# Ensure running as root
if [[ $EUID -ne 0 ]]; then
  echo "ERROR: Run as root (sudo ./run_experiment.sh)"
  exit 1
fi

# Ensure snapshotter binary exists
if [[ ! -f "$SNAPSHOTTER_BIN" ]]; then
  echo "ERROR: Snapshotter binary not found at $SNAPSHOTTER_BIN"
  exit 1
fi

# Ensure config exists, create minimal one if not
if [[ ! -f "$CONFIG" ]]; then
  mkdir -p "$(dirname "$CONFIG")"
  cat > "$CONFIG" <<EOF
metrics_address = "localhost:1338"

[background_fetch]
disable = false
silence_period_msec = 0
fetch_period_msec = 100
max_queue_size = 100
EOF
  log "Created config at $CONFIG"
fi

# ECR login - get token for ctr
ECR_TOKEN=$(aws ecr get-login-password --region "$REGION")
if [[ -z "$ECR_TOKEN" ]]; then
  echo "ERROR: Failed to get ECR token"
  exit 1
fi

# Init results file
echo "image,mode,trial,stall_count,elapsed_ms" > "$RESULTS"

log "=== LOD Oracle Experiment ==="
log "Images: ${#IMAGES[@]}, Trials: $TRIALS per mode"
log "Results: $RESULTS"
log ""

# Phase 1: Baseline (sequential + record access pattern)
log "=== Phase 1: Baseline (sequential fetcher + recording) ==="
for entry in "${IMAGES[@]}"; do
  tag="${entry%%|*}"
  cmd="${entry##*|}"
  rm -f "$ACCESS_LOG"  # fresh log per image

  for trial in $(seq 1 $TRIALS); do
    run_trial "$tag" "$cmd" "baseline" "$trial"
  done

  # Save access log for this image (for oracle phase)
  cp "$ACCESS_LOG" "/tmp/access_log_${tag}.log" 2>/dev/null || true
done

# Phase 2: Oracle (use recorded access order)
log ""
log "=== Phase 2: Oracle (access-order fetcher) ==="
for entry in "${IMAGES[@]}"; do
  tag="${entry%%|*}"
  cmd="${entry##*|}"

  # Use the access log from baseline phase
  if [[ -f "/tmp/access_log_${tag}.log" ]]; then
    cp "/tmp/access_log_${tag}.log" "$ACCESS_LOG"
  else
    log "  WARN: No access log for $tag, skipping oracle"
    continue
  fi

  for trial in $(seq 1 $TRIALS); do
    run_trial "$tag" "$cmd" "oracle" "$trial"
  done
done

# Phase 3: Control (sequential, no logging overhead)
log ""
log "=== Phase 3: Control (sequential, no logging) ==="
for entry in "${IMAGES[@]}"; do
  tag="${entry%%|*}"
  cmd="${entry##*|}"

  for trial in $(seq 1 $TRIALS); do
    run_trial "$tag" "$cmd" "control" "$trial"
  done
done

# Cleanup
stop_snapshotter

log ""
log "=== Experiment complete ==="
log "Results at: $RESULTS"
log "Total trials: $(( ${#IMAGES[@]} * 3 * TRIALS ))"
