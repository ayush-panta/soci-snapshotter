# SOCI on Windows — Full Investigation Summary

## Problem Statement

Windows container images are large (2-7GB). Cold start requires downloading and unpacking the entire image before the container can run. This takes 2-5 minutes. SOCI (Seekable OCI) solves this on Linux via lazy loading through FUSE. This investigation explores bringing lazy loading to Windows.

## Architecture Overview

### How SOCI works on Linux

```
Container reads file → FUSE (kernel) → soci-snapshotter (userspace) → HTTP range to registry → decompress → return bytes
```

Key components:
- `fs/layer/node.go` — FUSE callbacks (Linux-specific)
- `fs/reader/` — translates file reads into byte ranges (portable)
- `fs/span-manager/` — maps file offsets to compressed spans via zTOC (portable)
- `fs/remote/` — HTTP range requests to OCI registry (portable)
- `ztoc/` — table of contents enabling random access into gzip layers (portable)

### Proposed approach for Windows

Replace FUSE with WinFSP/cgofuse. The portable data pipeline (reader, span-manager, remote, ztoc) stays unchanged.

```
Container reads file → WinFSP (kernel driver) → cgofuse (userspace) → SOCI reader → HTTP range to ECR → decompress → return bytes
```

## PoC Results

### WinFSP Performance (measured)

Instance: m5.xlarge, Windows Server 2022, WinFSP v2.0, cgofuse v1.6.0 (nocgo mode)

**Test 1: 1MB file read from memory**

| | WinFSP | Local disk (NTFS) |
|---|---|---|
| Typical | ~13 ms | ~0.7 ms |
| Driver overhead | ~12 ms | — |

**Test 2: 100 × 4KB files sequential open+read from memory**

| | WinFSP | Local disk (NTFS) |
|---|---|---|
| Per file | 4.1 ms | 8.1 ms |

WinFSP is 2x faster than NTFS for many small files served from memory.

**Test 3: Single 4KB file repeated reads**

| | WinFSP | Local disk (NTFS) |
|---|---|---|
| First read (cold) | 34 ms | 71 ms |
| Subsequent (warm) | 1.5 ms | 8.6 ms |

**Comparison with Linux FUSE** (same test, same instance type):

| | FUSE (Linux) | WinFSP (Windows) |
|---|---|---|
| 1MB read overhead | ~0.4 ms | ~12 ms |

WinFSP is ~30x slower than FUSE for raw driver overhead on large reads. For small file access patterns (realistic container startup), the difference is much smaller and WinFSP actually outperforms NTFS local disk.

### Portability (measured)

SOCI's portable packages compile on Windows with two trivial patches:

1. `ztoc/compression/gzip_zinfo.c` — `#include <endian.h>` → `#ifdef _WIN32` no-op macros
2. `ztoc/compression/gzip_zinfo.go` — `C.long(offset)` → `C.offset_t(offset)` + `-D_FILE_OFFSET_BITS=64`

Packages confirmed building on Windows: `ztoc/`, `fs/reader/`, `fs/span-manager/`, `fs/remote/`, `cache/`, `metadata/`

### Container Pull Timing (measured)

Image: `mcr.microsoft.com/windows/servercore:ltsc2022` (~5GB)
Instance: m5.xlarge EKS Windows node

**Phase breakdown (via `ctr` on CimFS snapshotter):**

| Phase | Time | % of Total |
|-------|------|-----------|
| Download | 24.9s | 19% |
| Unpack (tar.gz → .cim) | 105.8s | **81%** |
| Total | 130.8s | 100% |

**Snapshotter comparison (via Kubernetes pod events):**

| Snapshotter | Total cold start | Improvement |
|---|---|---|
| Default (NTFS) | ~250s | baseline |
| CimFS | ~128s | 49% faster |
| SOCI lazy load (projected) | ~5-15s | ~95% faster |

## CimFS Investigation

### What CimFS is
A read-only projected filesystem for Windows that stores container layers as `.cim` archives instead of extracting to NTFS directories. Avoids per-file NTFS overhead (security descriptors, reparse points, MFT operations).

### CimFS + SOCI compatibility
- CimFS and SOCI lazy loading are **incompatible** — cimfs.sys expects the full .cim file on local disk. Cannot intercept reads via WinFSP.
- The `BlockCIMTypeDevice` option allows mounting from a block device, but creating a virtual block device on Windows requires a kernel driver — not practical.
- CimFS and SOCI serve different roles:
  - **CimFS** = faster unpack for full pulls (skip NTFS extraction)
  - **SOCI/WinFSP** = skip the pull entirely (lazy load)

### Proposed architecture

| Scenario | Mechanism |
|----------|-----------|
| SOCI index exists | Lazy load via WinFSP/cgofuse (instant start) |
| No SOCI index (fallback) | Full pull with CimFS snapshotter (faster unpack) |
| SOCI background fetch | Parallel span fetching while container runs (pure Go HTTP) |

## Parallel Unpack Investigation

### Finding: Unpack dominates cold start (81% of time)

Download (24.9s) is fast on AWS infrastructure. Unpack (105.8s) is the bottleneck — converting tar.gz to local layer format is CPU/IO intensive and currently **sequential** (each layer waits for parent).

### containerd parallel unpack support

- Requires snapshotter to support `"rebase"` capability
- Only `overlay` (Linux) and `erofs` (Linux) support it today
- **No Windows snapshotter supports parallel unpack**
- BlockCIM's merged-CIM architecture is compatible (independent layers, merge at mount) but unimplemented

### What's needed for parallel unpack on Windows

1. BlockCIM snapshotter implements `Commit` with deferred parent assignment
2. Register `"rebase"` capability in plugin init
3. Handle tombstone/merge semantics during rebase
4. Estimated effort: 2-3 weeks for someone familiar with containerd + hcsshim

### BlockCIM status
- Architecturally correct for parallel unpack
- Container startup failed in testing (scratch VHD issue) — needs debugging
- Not production-ready on current EKS AMIs

## Roadmap

### Phase 1: Prove end-to-end lazy loading (next)
- Wire SOCI reader pipeline to cgofuse on Windows
- Serve a real layer from ECR lazily through WinFSP
- Measure cold read latency (file request → bytes from registry)

### Phase 2: Benchmark against full pull
- Compare: lazy load start time vs full pull start time for real workloads
- Quantify: how many files does a typical Windows container touch at startup?

### Phase 3: Proposal
- Write internal proposal with PoC data
- Architecture: Layer interface refactor, Windows snapshotter design
- Scope: cgofuse node implementation, containerd integration, WCOW support
- Fallback path: CimFS for non-SOCI images

### Phase 4: Implementation (post-approval)
- Refactor `Layer` interface to decouple from go-fuse
- Implement cgofuse-based node callbacks using SOCI reader
- Build Windows containerd snapshotter (WinFSP mounts + WCOW composition)
- Add Windows CI/build targets

### Future optimizations
- Parallel unpack for CimFS (see detailed section below)
- Native WinFSP API (skip FUSE compat layer for ~1-3ms per-op improvement)
- CimFS lazy loading if Microsoft adds support
- Build .cim in background during lazy load for native-speed subsequent starts

## Parallel Unpack — Deep Dive

### Why it matters

Unpack dominates cold start: **81% of total time** (105.8s unpack vs 24.9s download, measured via `ctr`). Parallel unpack could reduce this significantly for multi-layer images.

### How parallel unpack works in containerd

Implemented in [PR #12332](https://github.com/containerd/containerd/pull/12332), merged Oct 2025 into containerd 2.2.0:

1. **Config:** `max_concurrent_unpacks` in transfer service config
2. **Capability:** Snapshotter must declare `"rebase"` capability
3. **Mechanism:** Layers are unpacked in parallel without parents. On commit, `WithParent` option is passed to assign the parent chain after data is written.
4. **Key architecture:** The rebase logic lives in the **storage layer** (`core/snapshots/storage/bolt.go`), not in individual snapshotters.

### Reviewer insight (dmcgowan):

> "This also means there is no other change needed in other snapshotters other than to set the capability."

This suggests adding parallel unpack to CimFS may be as simple as registering the `"rebase"` capability — the storage layer handles parent reassignment. However, data correctness (whiteouts, tombstones) without parent context during unpack needs validation.

### Current state of Windows snapshotters

| Snapshotter | Rebase support | Notes |
|---|---|---|
| `overlay` (Linux) | ✅ Yes | Implemented in PR #12332 |
| `erofs` (Linux) | ✅ Yes | Also registers rebase |
| `windows` (NTFS) | ❌ No | No capability registered. Layer extraction to NTFS dirs could work independently — needs investigation |
| `cimfs` (forked CIMs) | ❌ No | Forked CIMs bake parent content at creation time — may not be compatible |
| `blockcim` (merged CIMs) | ❌ No | Architecturally compatible (independent layers, merge at mount). Not available yet (see below) |

### BlockCIM availability

**Requires two things not yet available:**
1. **containerd 2.2.0+** — BlockCIM snapshotter was added in this version. EKS AMIs currently ship 2.1.6/2.1.7.
2. **Windows build 27766+** — `IsBlockCimSupported()` checks `build >= 27766`. Windows Server 2025 is build 26100 (too low).

From hcsshim source:
```go
func IsBlockCimSupported() bool {
    build := osversion.Build()
    // TODO(ambarve): Currently we are checking against a higher build number since there is no
    // official build with block CIM support yet.
    return build >= 27766 && cimfs.Supported()
}
```

Build 27766 is a Windows 11 Canary Channel preview (Jan 2025). Expected in a public Windows Server release in late 2026/2027.

**Tested and confirmed:**
- Win2022 (build 20348) + containerd 2.2.0: blockcim snapshotter = `skip`
- Win2025 (build 26100) + containerd 2.2.0: blockcim snapshotter = `skip`
- Neither OS meets the build 27766 requirement

### Actionable path for parallel unpack

1. **Investigate CimFS rebase compatibility** — Can CimFS layers be unpacked without parent context and rebased after? The data written during unpack may not depend on parent for standard layer extraction (tombstones are explicit).
2. **If yes:** Add `"rebase"` capability to CimFS snapshotter init (one line change), test thoroughly.
3. **If no:** Wait for BlockCIM (Windows build 27766+) which is architecturally designed for independent layer creation + merge.
4. **Alternative:** Investigate whether the standard `windows` snapshotter (NTFS extraction) could support rebase — layers are just directories of files, no structural parent dependency during extraction.

### Reference: `containerd/plugins/snapshots/windows/cimfs.go` init

Currently:
```go
func init() {
    registry.Register(&plugin.Registration{
        Type: plugins.SnapshotPlugin,
        ID:   "cimfs",
        InitFn: func(ic *plugin.InitContext) (any, error) {
            ic.Meta.Platforms = []ocispec.Platform{platforms.DefaultSpec()}
            return NewCimFSSnapshotter(ic.Properties[plugins.PropertyRootDir])
        },
    })
}
```

What it would need (if rebase is compatible):
```go
ic.Meta.Capabilities = append(ic.Meta.Capabilities, "rebase")
```

### Reference: EKS Windows AMI containerd versions

Source: https://docs.aws.amazon.com/eks/latest/userguide/eks-ami-versions-windows.html

| K8s version | containerd | BlockCIM possible? |
|---|---|---|
| 1.35 | 2.1.6 | ❌ (needs 2.2.0+) |
| 1.34 | 2.1.6 | ❌ |
| 1.33 | 1.7.30 | ❌ |
| ≤1.32 | 1.7.x | ❌ |

## Key Decisions

1. **WinFSP/cgofuse is the path for lazy loading** — proven viable, actionable now
2. **CimFS for fallback path** — better than NTFS when SOCI index unavailable (49% faster)
3. **CimFS and WinFSP don't compose** — separate code paths per layer
4. **Parallel unpack may be achievable for CimFS** — needs investigation of rebase compatibility, potentially a one-line change
5. **BlockCIM is the long-term parallel unpack path** — but requires unreleased Windows build + containerd 2.2.0
6. **SOCI index generation works for Windows images** — confirmed with Datadog servercore image
7. **Background .cim building during lazy load** — first run lazy (WinFSP), subsequent runs native (CimFS) once .cim is built

## Resources

- Windows PoC instance: `i-0f971b58701896919` (stopped)
- EKS cluster: `soci-windows-test` (us-west-2, account 299170649678, upgraded to k8s 1.35)
  - Linux node: `ip-192-168-53-44` (linux-system nodegroup)
  - Win2022 node: `ip-192-168-32-11` / `i-0575c92c824588c77` (windows-workers, containerd upgraded to 2.2.0)
  - Win2025 node: `ip-192-168-76-185` / `i-0d02cdc0c2970d55d` (win2025 nodegroup, containerd upgraded to 2.2.0)
- Test image: `299170649678.dkr.ecr.us-west-2.amazonaws.com/soci-on-windows:datadog-ltsc22`
- SOCI index (v1): pushed to ECR alongside image
- SOCI converted image (v2): `:datadog-ltsc22-soci`
- PoC code: `soci-contrib/poc/winfsp-bench/`
- CimFS test framework: `soci-contrib/WindowsContainerCIMFSContainerdSnapshotterPOC/`
- Parallel unpack PR: https://github.com/containerd/containerd/pull/12332
- BlockCIM snapshotter commit: `009625290` (containerd, Jul 2025)
- hcsshim CimFS code: `soci-contrib/hcsshim/pkg/cimfs/`
- containerd CimFS snapshotter: `containerd/plugins/snapshots/windows/cimfs.go`
