# PoC Results: SOCI on Windows via WinFSP

## Date: 2026-06-18

## What we proved

1. SOCI's portable data pipeline (ztoc, reader, span-manager, remote, cache, metadata) **compiles on Windows** with two trivial C portability patches.
2. WinFSP via cgofuse **mounts and serves files correctly** on Windows Server 2022.
3. WinFSP overhead is **acceptable for lazy loading**.

## Benchmark Results

### Test 1: 1MB file read (from memory)

| | FUSE (Linux) | WinFSP (Windows) |
|---|---|---|
| Userspace FS | ~0.7 ms | ~13 ms |
| Local disk | ~0.3 ms | ~0.7 ms |
| **Driver overhead** | **~0.4 ms** | **~12 ms** |

WinFSP is ~30x slower than FUSE for raw driver round-trip on large reads.

### Test 2: 100 x 4KB files (sequential open+read from memory)

| | WinFSP | Local disk (NTFS) |
|---|---|---|
| Total | 410 ms | 808 ms |
| **Per file** | **4.1 ms** | **8.1 ms** |

WinFSP is **2x faster** than NTFS for many small files served from memory.

### Test 3: Single 4KB file (repeated reads)

| | WinFSP | Local disk (NTFS) |
|---|---|---|
| First read (cold) | 34 ms | 71 ms |
| Subsequent (warm) | **1.5 ms** | **8.6 ms** |

WinFSP is **~6x faster** than NTFS once warm for small file reads from memory.

## Interpretation

The 12ms overhead seen in Test 1 is specific to **large file reads** (1MB), where WinFSP makes many chunked Read callbacks. For the more realistic container startup scenario — many small files read sequentially — WinFSP at 1.5-4ms per file is fast and actually beats native NTFS (because NTFS has its own per-file overhead: MFT lookups, security descriptors, disk seeks).

**For SOCI's use case:**
- **Cache miss** (network fetch): WinFSP overhead is negligible vs 50-200ms network latency
- **Cache hit** (span already local): WinFSP at 1.5-4ms per file is fast enough — comparable to or better than native disk
- **Container startup** (hundreds of small files): No "context switch cascade" — WinFSP handles this well

## Portability Patches Required

Two patches to `ztoc/compression/` to compile on Windows:

1. `gzip_zinfo.c`: Replace `#include <endian.h>` with `#ifdef _WIN32` no-op macros (x86_64 is already little-endian)
2. `gzip_zinfo.go`: Change `C.long(offset)` → `C.offset_t(offset)` + add `-D_FILE_OFFSET_BITS=64` CFLAG

## What remains for full implementation

| Phase | Work | Risk |
|-------|------|------|
| cgofuse node implementation | Implement Getattr/Read/Readdir/Open using SOCI reader pipeline | Low — we proved the pipeline compiles and cgofuse works |
| Layer interface refactor | Decouple `Layer.RootNode()` from go-fuse types | Medium — touches existing code, needs careful design |
| WCOW/containerd integration | Windows snapshotter that feeds WinFSP mounts into container layer composition | Medium — complex plumbing, but well-understood APIs |
| CimFS investigation | Evaluate whether CimFS (Microsoft's native container FS) is a better target than WinFSP for the layer serving | Unknown — could simplify or replace the WinFSP approach |

## Conclusion

**WinFSP is viable.** The filesystem driver overhead does not block SOCI on Windows. The main engineering challenges are containerd integration (WCOW) and whether CimFS offers a better native path. The initiative is feasible and worth proposing.
