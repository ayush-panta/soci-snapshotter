# PoC Plan: SOCI on Windows via WinFSP

## Test Image

- Source: `public.ecr.aws/datadog/agent:latest-servercore-ltsc2022`
- Private ECR: `299170649678.dkr.ecr.us-west-2.amazonaws.com/soci-on-windows:datadog-ltsc22`

## Goal

Prove that WinFSP can serve container image layer files lazily (on demand from ECR) with acceptable overhead compared to FUSE on Linux.

## Phases

### Phase 1: Setup

**From Mac (local):**
```bash
crane copy public.ecr.aws/datadog/agent:latest-servercore-ltsc2022 \
  299170649678.dkr.ecr.us-west-2.amazonaws.com/soci-on-windows:datadog-ltsc22
```

**On Linux `soci-lab` (`i-00da41761a8b7a660`):**
```bash
# Pull the image into containerd
sudo ctr image pull 299170649678.dkr.ecr.us-west-2.amazonaws.com/soci-on-windows:datadog-ltsc22

# Generate SOCI index (v2)
sudo soci create 299170649678.dkr.ecr.us-west-2.amazonaws.com/soci-on-windows:datadog-ltsc22

# Push SOCI index to ECR
sudo soci push 299170649678.dkr.ecr.us-west-2.amazonaws.com/soci-on-windows:datadog-ltsc22
```

**Windows instance (see 02-windows-dev-instance.md):**
1. Launch Windows Server 2022 instance
2. Install Go, WinFSP (Developer), Git, MinGW (for CGo)
3. Verify cgofuse compiles (`go build` the memfs example)
4. Clone soci-snapshotter

### Phase 2: Wire SOCI reader to cgofuse

1. Verify portable packages compile on Windows:
   ```
   ztoc/, fs/reader/, fs/span-manager/, fs/remote/, metadata/, cache/
   ```
2. Write a cgofuse `FileSystemInterface` that:
   - `Readdir` / `Getattr` → serves file metadata from zTOC
   - `Read` → calls `reader.Reader.OpenFile(id).ReadAt()` → span-manager → HTTP range → ECR
3. Mount it as a drive/directory on Windows
4. Verify: `dir` and `type <file>` return correct content from the mounted layer

### Phase 3: Benchmark

#### What to measure

| Metric | How |
|--------|-----|
| Mount time | Time from `Mount()` to mount ready |
| Cold read latency | Time to read a file with empty cache (first HTTP fetch) |
| Warm read latency | Time to read same file again (cached span) |
| Throughput | Sequential read of N files, total MB/s |

#### Comparison baseline

Run same measurements on Linux (`soci-lab`) using SOCI's existing FUSE implementation against the same image/layer.

#### Pass/Fail

| Criterion | Pass | Fail |
|-----------|------|------|
| WinFSP mount overhead | < 1 second | > 5 seconds |
| Cold read latency vs FUSE | Within 2x of FUSE | > 5x of FUSE |
| Warm read latency | < 5ms | > 50ms |
| No goroutine blocking | Concurrent reads work | Reads serialize unexpectedly |

## What success proves

WinFSP is a viable mechanism for lazy loading on Windows. Remaining work (Layer interface refactor, WCOW containerd integration) is implementation effort, not a feasibility risk.

## What failure means

Alternatives to investigate:
- Windows Projected File System (ProjFS)
- Native WinFSP API (skip FUSE compat layer)
- Kernel minifilter driver (last resort)
