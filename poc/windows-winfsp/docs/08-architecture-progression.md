# SOCI on Windows — Architecture Progression

## Phase 0: Basic mount and vend ✅ DONE

**Goal:** Prove WinFSP/cgofuse works, measure overhead.

**What we built:**
- cgofuse filesystem serving a fake blob from memory
- Benchmark harness comparing WinFSP vs local NTFS vs Linux FUSE

**What we proved:**
- WinFSP overhead is ~1.5-4ms per small file (acceptable given network will dominate)
- cgofuse builds on Windows in nocgo mode
- SOCI portable packages compile with 2 patches

---

## Phase 1: Serve a real remote layer

**Goal:** Fetch one layer from ECR via HTTP Range requests, serve files through WinFSP.

**What to build:**
```
┌─────────────────────────────────────────────────────┐
│  cgofuse callbacks (Getattr, Readdir, Open, Read)   │
├─────────────────────────────────────────────────────┤
│  File tree (parsed from zTOC)                       │
├─────────────────────────────────────────────────────┤
│  Span manager (maps file offsets → blob ranges)     │
├─────────────────────────────────────────────────────┤
│  HTTP Range fetcher (ECR auth + Range GET)          │
└─────────────────────────────────────────────────────┘
```

**Steps:**
1. Fetch zTOC artifact from ECR (already pushed)
2. Parse zTOC → build in-memory file tree (path, size, mode, offsets)
3. Implement span fetcher: given (span_offset, length), do HTTP Range GET on the layer blob
4. Implement cgofuse `Getattr` → lookup file tree
5. Implement cgofuse `Readdir` → list directory entries
6. Implement cgofuse `Read` → decompress span from remote blob
7. Mount, `dir` / `type` a file, measure cold read latency

**Key decisions:**
- No caching yet (every read goes to network)
- Single layer only (not full image)
- Manual: no integration with containerd, just a standalone Go binary
- Auth: use ECR token from `aws ecr get-login-password`

**Success metric:** Read a file from a real Windows layer via WinFSP in <500ms cold.

---

## Phase 2: Add caching + multi-layer

**Goal:** Make repeated reads fast, support a full image (multiple layers).

**What to add:**
```
┌─────────────────────────────────────────────────────┐
│  cgofuse callbacks                                  │
├─────────────────────────────────────────────────────┤
│  Union view (overlay multiple layer file trees)     │
├─────────────────────────────────────────────────────┤
│  File tree per layer (from zTOC)                    │
├─────────────────────────────────────────────────────┤
│  Local span cache (disk-backed, LRU)                │
├─────────────────────────────────────────────────────┤
│  Span manager                                       │
├─────────────────────────────────────────────────────┤
│  HTTP Range fetcher                                 │
└─────────────────────────────────────────────────────┘
```

**Steps:**
1. Add on-disk span cache (write fetched spans to local file, serve subsequent reads from disk)
2. Build union file tree — layer N overrides layer N-1, handle whiteouts/tombstones
3. Mount full image as single WinFSP volume
4. Run a real Windows container binary against the mount (e.g., `cmd.exe /c dir`)

**Key decisions:**
- Cache granularity: per-span (aligned to zTOC spans, typically 4MB compressed)
- Whiteout handling: OCI `.wh.` prefix files → hide parent entry
- Windows-specific: handle case-insensitive lookups, reparse points, security descriptors

**Success metric:** Full image mounted, `cmd.exe` starts, repeated file reads hit cache (<5ms).

---

## Phase 3: SOCI reader integration

**Goal:** Use SOCI's actual reader/span-manager code instead of our custom implementation.

**What changes:**
- Replace our custom span fetcher + file tree with SOCI's `reader.Reader`
- Use SOCI's `metadata` package for file tree
- Use SOCI's `spanmanager` for fetching + caching
- Use SOCI's `remote` package for registry auth/resolver

**Refactoring needed in soci-snapshotter:**
```
Current (Linux):
  fs/layer/layer.go → creates go-fuse nodes → serves via /dev/fuse

Needed (portable):
  fs/layer/layer.go → exposes Reader interface
  fs/layer/node_linux.go → go-fuse adapter (existing)
  fs/layer/node_windows.go → cgofuse adapter (new)
```

**Steps:**
1. Refactor `Layer` interface to decouple from go-fuse types
2. Extract file-serving logic into platform-agnostic `Reader` interface:
   - `Stat(path) → FileInfo`
   - `ReadDir(path) → []DirEntry`
   - `ReadFile(path, buf, offset) → n, error`
3. Build cgofuse adapter that calls Reader interface
4. Test: same layer, Linux go-fuse vs Windows cgofuse produce identical file content

**Success metric:** SOCI's real reader pipeline (with background fetch, prefetch, verification) running behind cgofuse.

---

## Phase 4: containerd snapshotter integration

**Goal:** containerd calls our snapshotter, which mounts layers via WinFSP automatically.

**Architecture:**
```
containerd pull (with SOCI index)
  │
  ▼
SOCI Windows snapshotter plugin
  │
  ├─ Prepare(key, parent) → mount WinFSP volume for layer
  │    1. Fetch zTOC for this layer
  │    2. Initialize reader + span manager
  │    3. Mount cgofuse at snapshot dir
  │    4. Return mount info to containerd
  │
  ├─ Mounts(key) → return existing mount
  │
  ├─ Commit(key, name) → finalize layer (optional: trigger background .cim build)
  │
  └─ Remove(key) → unmount WinFSP, cleanup cache
```

**Steps:**
1. Implement snapshotter gRPC service (or in-process plugin)
2. Wire `Prepare` → fetch zTOC → mount WinFSP
3. Wire `Remove` → unmount + cleanup
4. Handle multi-layer: each layer is its own WinFSP mount, containerd/hcsshim composes them
5. Handle fallback: if no SOCI index exists, fall through to default snapshotter (CimFS)

**Key challenge:** Windows container layer composition. Unlike Linux overlayfs (which composites in-kernel), Windows uses:
- WCIFS filter driver (composes layer directories at runtime)
- Or CimFS merged views

Need to determine if WCIFS can compose WinFSP-mounted directories, or if we need a single unified WinFSP mount with union logic built in.

**Success metric:** `ctr images pull --snapshotter soci` works, container starts with lazy-loaded layers.

---

## Phase 5: Production hardening

**Goal:** Reliable, performant, observable.

**What to add:**
- Prefetch heuristics (predict access patterns, fetch ahead of reads)
- Background .cim building (after lazy load, build CIM for future starts)
- Metrics/tracing (cold read latency, cache hit rate, span fetch time)
- Error handling (network failures → retry with backoff, WinFSP timeout handling)
- Graceful degradation (if WinFSP mount fails, fall back to full pull)
- Resource limits (max concurrent fetches, cache size cap, memory bounds)
- Integration tests on EKS Windows nodes

---

## Open Questions

| # | Question | Impacts |
|---|----------|---------|
| 1 | Can WCIFS compose WinFSP-mounted layer directories? | Phase 4 architecture (single union mount vs per-layer mounts) |
| 2 | How does hcsshim expect layer mounts to be structured? | Phase 4 mount returns |
| 3 | Should we target containerd remote snapshotter (gRPC) or in-process plugin? | Phase 4 implementation |
| 4 | Can we reuse SOCI's existing background fetch / verification without go-fuse? | Phase 3 refactoring scope |
| 5 | Windows security descriptors in tar — does SOCI's reader preserve them? | Phase 2 correctness |
