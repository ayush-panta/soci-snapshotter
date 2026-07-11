# Step 1: Where Generic Ends and OS-Specific Begins

## The Boundary

The codebase splits cleanly into two zones. The dividing line is **the FUSE server creation** in `fs/fs.go:setupFuseServer()`.

Everything *above* that function (data fetching, decompression, indexing) has zero Linux-specific imports.
Everything *at and below* it (FUSE node callbacks, mount syscalls, overlayfs integration) is deeply Linux-specific.

## Package-by-Package Breakdown

### Conceptually Portable (no syscall/fuse/unix imports)

| Package | What it does | Linux-specific? |
|---------|-------------|-----------------|
| `fs/remote/` | HTTP range requests to OCI registries, blob fetching | **No** — pure HTTP |
| `fs/reader/` | Wraps span manager to serve byte ranges for a layer | **No** — pure Go |
| `fs/span-manager/` | Maps file offsets → compressed spans via zTOC | **No** — pure data structures |
| `fs/backgroundfetcher/` | Prefetches spans in background goroutines | **No** — pure Go |
| `ztoc/` | Parses and creates zTOC (table of contents for gzip layers) | **No** — compression math |
| `soci/` | SOCI index creation and management | **No** — OCI artifact logic |
| `metadata/` | Stores file metadata (names, sizes, modes) from tar headers | **No** — database (bbolt) |
| `cache/` | Content-addressable cache for fetched spans | **No** — file I/O only |

### Fundamentally Linux-Specific

| Package/File | What it does | Why it's Linux-specific |
|---|---|---|
| `fs/layer/node.go` | Implements FUSE node callbacks (`Getattr`, `Read`, `Lookup`, `Readlink`, `Readdir`, `Open`, `Getxattr`, `Listxattr`) | Imports `github.com/hanwen/go-fuse/v2/fs` and `fuse`. Every method signature returns fuse-specific types (`fuse.Attr`, `fusefs.InodeEmbedder`, `fuse.Status`). |
| `fs/fs.go` → `setupFuseServer()` | Creates FUSE server, mounts filesystem, calls `fusermount` | Direct FUSE server instantiation. Uses `syscall`, `golang.org/x/sys/unix`. References `fusermount` binary. |
| `fs/layer/layer.go` → `Layer` interface | `RootNode()` returns `fusefs.InodeEmbedder` | The **interface itself** leaks FUSE types upward. This is the key coupling. |
| `snapshot/snapshot.go` | containerd snapshotter plugin (overlayfs mounts) | Uses `syscall`, `mountinfo`, `overlayutils`. Assumes Linux overlay filesystem. |

## The Key Coupling Point

The `Layer` interface in `fs/layer/layer.go` line 94:

```go
RootNode(baseInode uint32, idMapper idtools.IDMap) (fusefs.InodeEmbedder, error)
```

This means the entire layer resolution pipeline — which is otherwise portable — is forced to produce a FUSE-specific type. Everything that consumes a `Layer` must know about `go-fuse`.

## Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│  PORTABLE ZONE                                                  │
│                                                                 │
│  registry ─→ fs/remote/ (HTTP range req)                        │
│                  │                                              │
│                  ▼                                              │
│  ztoc/ (parse index) ─→ fs/span-manager/ (offset → span)       │
│                              │                                  │
│                              ▼                                  │
│  cache/ ◄──── fs/reader/ (serve byte ranges)                    │
│                              │                                  │
│  metadata/ (file tree)       │                                  │
│         │                    │                                  │
├─────────┼────────────────────┼──────────────────────────────────┤
│         ▼                    ▼         LINUX-SPECIFIC ZONE      │
│                                                                 │
│  fs/layer/layer.go  ─── Layer interface (leaks fusefs types)    │
│         │                                                       │
│         ▼                                                       │
│  fs/layer/node.go  ─── FUSE callbacks (Getattr, Read, etc.)    │
│         │                                                       │
│         ▼                                                       │
│  fs/fs.go:setupFuseServer() ─── go-fuse server + mount         │
│         │                                                       │
│         ▼                                                       │
│  snapshot/snapshot.go ─── overlayfs mounts, containerd plugin   │
└─────────────────────────────────────────────────────────────────┘
```

## What This Means

The seam you'd cut is at the `Layer` interface. Specifically:
- `RootNode()` is the method that forces FUSE coupling into the interface
- Below that method: platform-specific filesystem callbacks
- Above that method: portable data pipeline (fetch, decompress, cache, serve bytes)

The challenge isn't that lots of code is Linux-specific — it's actually a small surface. The challenge is that the interface *between* the portable zone and the OS-specific zone currently has FUSE baked into its type signature.
