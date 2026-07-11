# SOCI on Windows — WinFSP Lazy Loading Focus

## Status: Phase 1 of PoC (ready to wire real SOCI reader to cgofuse)

## What we've proven

1. **WinFSP performance is viable** — measured ~1.5-4ms per small file read, negligible overhead for lazy loading use case
2. **SOCI portable packages compile on Windows** — ztoc, reader, span-manager, remote, cache, metadata all build with two trivial C patches
3. **cgofuse builds and mounts correctly** — nocgo mode, no MinGW needed for cgofuse itself (only for SOCI's zlib C code)
4. **SOCI index works for Windows images** — generated and pushed zTOC for Datadog servercore ltsc2022 image
5. **CimFS is not compatible with lazy loading** — cimfs.sys expects full .cim on disk, cannot be intercepted by userspace FS

## Architecture

```
SOCI lazy load path (WinFSP):
  Container reads file → WinFSP driver → cgofuse (userspace) → SOCI reader → span-manager → HTTP Range → ECR

Fallback path (CimFS, handled by colleague):
  Full pull → unpack tar.gz → write .cim → mount via cimfs.sys
```

These are separate code paths per layer. WinFSP for lazy-loaded layers, CimFS for fully-pulled layers.

## WinFSP Benchmark Data

Instance: m5.xlarge, Windows Server 2022, WinFSP v2.0, cgofuse v1.6.0 (nocgo mode)

### 1MB file from memory
| | WinFSP | Local NTFS | FUSE (Linux) |
|---|---|---|---|
| Read time | ~13 ms | ~0.7 ms | ~0.7 ms |
| Overhead | ~12 ms | — | ~0.4 ms |

### 100 × 4KB files from memory (sequential open+read)
| | WinFSP | Local NTFS |
|---|---|---|
| Per file | 4.1 ms | 8.1 ms |
| WinFSP faster because no NTFS metadata overhead |

### Single 4KB file (repeated)
| | WinFSP | Local NTFS |
|---|---|---|
| Cold (first access) | 34 ms | 71 ms |
| Warm (subsequent) | 1.5 ms | 8.6 ms |

### Why 12ms for large reads vs 1.5ms for small reads
WinFSP chunks large reads into ~64KB callbacks. A 1MB read = ~16 round trips. Small file = 1 round trip.

## Portability Patches Required

Two patches to `ztoc/compression/`:

1. **`gzip_zinfo.c`** — replace `#include <endian.h>`:
```c
#ifdef _WIN32
#include <stdint.h>
#define htole64(x) (x)
#define le64toh(x) (x)
#define htole32(x) (x)
#define le32toh(x) (x)
#else
#include <endian.h>
#endif
```

2. **`gzip_zinfo.go`** — fix type mismatch:
- `C.long(offset)` → `C.offset_t(offset)`
- Add `-D_FILE_OFFSET_BITS=64` to CFLAGS

## What's next (Phase 1)

Wire the real SOCI reader pipeline to cgofuse. Serve a real layer from ECR lazily through WinFSP.

### What this requires:
1. A Go program on the Windows instance that:
   - Fetches the zTOC for one layer from ECR
   - Initializes metadata store (parse zTOC → file tree)
   - Initializes span-manager + remote resolver (pointed at layer blob in ECR)
   - Initializes reader
2. Implements cgofuse callbacks wired to the reader:
   - `Getattr` → metadata store lookup
   - `Readdir` → metadata store directory listing
   - `Open` → reader.OpenFile(id)
   - `Read` → reader.ReadAt(buf, offset) → span-manager → ECR
3. Mount, browse, read a file, measure cold read latency

### Dependencies available:
- Windows instance: `i-0f971b58701896919` (stopped, has Go + WinFSP + MinGW + soci-snapshotter cloned)
- SOCI index in ECR: `299170649678.dkr.ecr.us-west-2.amazonaws.com/soci-on-windows:datadog-ltsc22`
- zTOC pushed as OCI referrer artifact
- All portable packages confirmed compiling on the instance

### Key challenge:
Bootstrapping the SOCI reader pipeline manually (without `fs/fs.go` or `fs/layer/layer.go` which import go-fuse). Need to directly instantiate:
- `metadata.NewReader()` from the zTOC
- `spanmanager.NewSpanManager()` with remote blob resolver
- `reader.NewReader()` wrapping the span manager
- Then wire these into cgofuse callbacks

## Future phases

### Phase 2: Benchmark vs full pull
- Measure: WinFSP lazy start time vs 128s CimFS cold pull
- Target: <15s to first file read

### Phase 3: Proposal writeup
- Architecture: Layer interface refactor, Windows snapshotter design
- Evidence: PoC data
- Scope estimate

### Phase 4: Implementation (post-approval)
- Refactor `Layer` interface (decouple from go-fuse)
- Build cgofuse node callbacks using SOCI reader
- Build Windows containerd snapshotter (WinFSP + WCOW integration)
- CI: Windows build targets

### Long-term optimization
- Build .cim in background during lazy load → subsequent starts use native CimFS (no WinFSP overhead)
- Native WinFSP API (skip FUSE compat layer, ~1-3ms improvement per op)

## Resources

| Resource | Location |
|----------|----------|
| Windows PoC instance | `i-0f971b58701896919` (stopped, Win2022, m5.xlarge) |
| EKS cluster | `soci-windows-test` (us-west-2, k8s 1.35) |
| Test image | `299170649678.dkr.ecr.us-west-2.amazonaws.com/soci-on-windows:datadog-ltsc22` |
| SOCI index (v1) | Pushed to ECR as referrer |
| SOCI v2 image | `:datadog-ltsc22-soci` |
| PoC code (mock FS) | `soci-contrib/poc/winfsp-bench/` |
| soci-snapshotter source | `soci-contrib/soci-snapshotter/` |
| cgofuse on instance | `C:\dev\cgofuse\` (builds in nocgo mode) |
| soci-snapshotter on instance | `C:\dev\soci-snapshotter\` (portable packages build with CGO_ENABLED=1 + MinGW) |
| Linux FUSE benchmark | `soci-lab:~/fuse-bench/` |
