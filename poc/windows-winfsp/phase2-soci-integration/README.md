# Phase 2: SOCI Integration

Same as Phase 1 but uses SOCI's real packages instead of custom implementations.

## What changed from Phase 1

| Component | Phase 1 (custom) | Phase 2 (SOCI) |
|-----------|-------------------|----------------|
| File tree | `FileTree` (flat map) | `metadata.Reader` (bbolt DB, ID-based) |
| Span fetching | `SpanFetcher` (custom HTTP Range + decompress) | `span-manager.SpanManager` (with on-disk cache) |
| File reading | Manual offset math | `reader.Reader.OpenFile(id).ReadAt()` |
| Caching | None | `cache.DirectoryCache` (LRU, disk-backed) |
| Remote blob | Custom `httpReaderAt` | Custom `httpReaderAt` (same — SOCI's remote pkg needs containerd resolver, overkill for PoC) |

## What we get for free from SOCI

- **Span caching** — first read fetches from network, subsequent reads from disk
- **Correct file ID resolution** — metadata.Reader handles all edge cases
- **Span verification** — optional digest verification per span
- **Proper TOC handling** — handles all file types, xattrs, hardlinks correctly

## Build (on Windows instance)

```powershell
$env:Path = "C:\Program Files\Go\bin;C:\mingw64\bin;C:\Program Files\Amazon\AWSCLIV2;" + $env:Path
$env:CGO_ENABLED = "1"
$env:CC = "gcc"
$env:CGO_CFLAGS = "-IC:\PROGRA~2\WinFsp\inc\fuse"
$env:CGO_LDFLAGS = "-LC:\PROGRA~2\WinFsp\lib"
$env:GONOSUMCHECK = "*"
$env:GONOSUMDB = "*"
$env:GOFLAGS = "-mod=mod"
Set-Location C:\dev\phase2-soci-integration
go build -o phase2.exe .
```

## Run

```powershell
.\phase2.exe -mount W: -cache-dir C:\dev\soci-cache
```

## Expected behavior

- First file read: ~100-500ms (network fetch + decompress + cache write)
- Second read of same file: ~1-4ms (cache hit, WinFSP overhead only)
- Directory listings: instant (metadata in bbolt DB)
