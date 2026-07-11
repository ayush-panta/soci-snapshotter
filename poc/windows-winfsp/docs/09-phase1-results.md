# Phase 1 Results: Real Layer Lazy Loading via WinFSP

## Date: 2026-07-10

## Summary

Successfully lazy-loaded a real Windows container layer (1.49 GB compressed) from ECR through SOCI's decompression engine and WinFSP. Files are served on-demand via HTTP Range requests — no full pull, no unpack step.

## Test Setup

- **Instance:** `i-0f971b58701896919` (m5.xlarge, Windows Server 2022)
- **Image:** `299170649678.dkr.ecr.us-west-2.amazonaws.com/soci-on-windows:datadog-ltsc22`
- **Layer:** Largest layer, sha256:3cc21a... (1,489,019,076 bytes compressed)
- **zTOC:** sha256:12e762... (83,181,248 bytes), 60,858 files, 808 spans
- **Tools:** Go 1.23.4, MinGW 13.2.0, WinFSP v2.0, cgofuse v1.6.0

## Results

### Startup timing (auth → mounted filesystem)

| Step | Time |
|------|------|
| ECR auth token | ~1s |
| Manifest resolution (list → platform) | <1s |
| SOCI index lookup (referrers API) | <1s |
| zTOC download (83 MB) | ~1s |
| zTOC parse + file tree build | <1s |
| **Total: auth to mount** | **~3 seconds** |

### File read timing

| Operation | Details |
|-----------|---------|
| Read `hosts` (824 bytes) | Fetched span #323 (1.77 MB compressed) from ECR, decompressed, served — all within 1 second |
| Directory listing (`dir W:\Files`) | Instant (served from in-memory tree) |

### Comparison to full pull

| | Full pull (CimFS) | Lazy load (this PoC) |
|---|---|---|
| Time to first file access | **128 seconds** | **~3 seconds** |
| Data transferred before first access | 1.49 GB (entire layer) | 83 MB (zTOC) + 1.77 MB (one span) |
| Unpack step | 105.8 seconds | Zero |
| Speedup | — | **~40x faster to first access** |

## What was proven

1. ✅ WinFSP/cgofuse can serve a real container layer filesystem
2. ✅ SOCI's zTOC correctly maps 60,858 files across 808 spans
3. ✅ SOCI's C/zlib decompression (`ExtractDataFromBuffer`) works on Windows with MinGW
4. ✅ HTTP Range requests to ECR blob work for span-level fetching
5. ✅ OCI Referrers API correctly resolves SOCI index → zTOC for a given layer
6. ✅ Directory listings are instant (served from parsed zTOC metadata)
7. ✅ File content is correct (hosts file matches expected Windows Server Core content)

## What was NOT proven (yet)

- Multi-span file reads (files larger than one span, ~4MB)
- Byte-for-byte correctness of all files (only verified hosts file)
- Cross-span-boundary reads
- Concurrent read performance
- Hardlinks and symlinks
- Large-scale directory scanning (antivirus, indexer)
- Running an executable from the mount

## SOCI packages used

| Package | Used | Role |
|---------|------|------|
| `ztoc` (flatbuffers parser) | ✅ | Parsed the 83MB zTOC |
| `ztoc/compression` (C/zlib) | ✅ | Created Zinfo, decompressed spans |
| `metadata` | ❌ Custom replacement | File tree (our `FileTree`) |
| `fs/span-manager` | ❌ Custom replacement | Span fetching (our `SpanFetcher`) |
| `fs/reader` | ❌ Custom replacement | Read orchestration |
| `cache` | ❌ Not implemented | No caching — every read hits network |
| `remote` | ❌ Custom replacement | Registry auth (our `registry.go`) |

## Build requirements

```powershell
$env:CGO_ENABLED = "1"
$env:CC = "gcc"
$env:CGO_CFLAGS = "-IC:\PROGRA~2\WinFsp\inc\fuse"
$env:CGO_LDFLAGS = "-LC:\PROGRA~2\WinFsp\lib"
$env:GONOSUMCHECK = "*"
$env:GONOSUMDB = "*"
```

## Code location

- Source: `soci-contrib/poc/phase1-real-layer/` (5 Go files + go.mod)
- Binary on instance: `C:\dev\phase1-real-layer\phase1.exe` (31 MB)
- Logs on instance: `C:\dev\phase1-real-layer\stderr.log`

## Next step

Replace custom implementations with SOCI's real packages (`metadata.Reader`, `span-manager`, `cache`, `remote`) to get:
- On-disk span caching (fetch once, read from disk after)
- Background span prefetching
- Token refresh and retry logic
- Proper file verification
