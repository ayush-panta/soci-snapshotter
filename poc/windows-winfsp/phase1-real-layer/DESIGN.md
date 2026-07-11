# Phase 1: Real Layer Serve — Design

## Module layout

```
poc/phase1-real-layer/
├── main.go           # Entry point: parse args, init components, mount
├── registry.go       # ECR auth + fetch zTOC artifact + get layer blob URL
├── filetree.go       # Parse zTOC FileMetadata → path-indexed tree
├── spanfetch.go      # HTTP Range fetch + decompress using SOCI's compression pkg
├── fusefs.go         # cgofuse callbacks wired to filetree + spanfetch
└── go.mod
```

## Data flow

```
main():
  1. Get ECR auth token
  2. Resolve SOCI index → get zTOC digest for target layer
  3. Fetch zTOC blob from ECR
  4. ztoc.Unmarshal(blob) → *Ztoc
  5. Build FileTree from ztoc.TOC.FileMetadata
  6. Create Zinfo from ztoc.Checkpoints
  7. Create SpanFetcher(layerBlobURL, zinfo, compressedArchiveSize)
  8. Create FuseFS(fileTree, spanFetcher)
  9. Mount at W:\
  10. Block until unmount
```

## Key data structures

```go
// FileTree — path-indexed for O(1) lookup
type FileNode struct {
    Name     string
    IsDir    bool
    Size     int64
    Mode     os.FileMode
    ModTime  time.Time
    LinkName string
    // For files: position in uncompressed tar stream
    UncompressedOffset int64
    UncompressedSize   int64
    // Directory children
    Children map[string]*FileNode
}

type FileTree struct {
    Root *FileNode
}

func (ft *FileTree) Lookup(path string) *FileNode
func (ft *FileTree) ReadDir(path string) []*FileNode
```

```go
// SpanFetcher — fetches + decompresses spans from remote blob
type SpanFetcher struct {
    blobURL               string
    httpClient            *http.Client
    zinfo                 *compression.GzipZinfo
    compressedArchiveSize int64
}

// ReadFile fetches the bytes for a file at the given uncompressed offset/size
func (sf *SpanFetcher) ReadFile(uncompOffset, uncompSize int64, buf []byte, readOffset int64) (int, error)
```

## Dependencies

- `github.com/awslabs/soci-snapshotter/ztoc` — Unmarshal (flatbuffers, pure Go)
- `github.com/awslabs/soci-snapshotter/ztoc/compression` — GzipZinfo, ExtractDataFromBuffer (CGO)
- `github.com/winfsp/cgofuse` — FUSE filesystem
- `github.com/aws/aws-sdk-go-v2` — ECR auth token
- Standard library: `net/http`, `encoding/json`

## CGO requirement

The `compression` package uses C code for span-based decompression (zlib with dictionary resume).
Build requires: `CGO_ENABLED=1` + MinGW (already installed on Windows instance).

## Auth flow

1. `aws ecr get-authorization-token` → base64(user:pass)
2. Use Docker Registry HTTP API v2:
   - `GET /v2/{repo}/manifests/{tag}` (get image manifest)
   - `GET /v2/{repo}/referrers/{digest}` (get SOCI index referrer)
   - `GET /v2/{repo}/blobs/{ztoc_digest}` (fetch zTOC)
   - `GET /v2/{repo}/blobs/{layer_digest}` with Range header (fetch spans)

## Simplifications for Phase 1

- Single layer only (pick one layer from the image, e.g., the largest)
- No caching (every file read → network fetch)
- No prefetch
- No verification (skip tar header check)
- Hardcoded image reference + layer digest (avoid complex manifest parsing initially)
- Read-only filesystem
