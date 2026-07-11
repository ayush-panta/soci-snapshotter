# Roadmap: SOCI on Windows PoC

## What we've done (Phase 0) ✓

- Proved WinFSP performance is viable
- Proved SOCI portable packages compile on Windows (with 2 trivial patches)
- Proved cgofuse mounts and serves files correctly
- Set up Windows dev instance with full toolchain
- Pushed Datadog ltsc2022 image + SOCI index to ECR

## Phase 1: Serve one real layer through WinFSP

**Goal:** Mount a single Datadog image layer via cgofuse, backed by the SOCI reader pipeline. Read a real file lazily from ECR.

**Steps:**
1. Write a Go program on the Windows instance that:
   - Fetches the zTOC for one layer from ECR (HTTP GET of the artifact)
   - Initializes metadata store (parse zTOC → file tree)
   - Initializes span-manager + remote resolver (pointed at the layer blob in ECR)
   - Initializes reader
2. Implement cgofuse callbacks wired to the reader:
   - `Getattr` → metadata store lookup
   - `Readdir` → metadata store directory listing
   - `Open` → reader.OpenFile(id)
   - `Read` → reader.ReadAt(buf, offset) → span-manager → ECR
3. Mount at `T:\`, browse the layer, read a file
4. Measure: time from file request to bytes returned (cold cache = network fetch)

**Proves:** End-to-end lazy loading from ECR through WinFSP on Windows.

## Phase 2: Benchmark against full pull

**Goal:** Quantify the startup time improvement.

**Steps:**
1. Measure: time to pull entire Datadog image traditionally (full download + decompress)
2. Measure: time to mount via WinFSP + read the first N files a container would need at startup
3. Compare the two

**Proves:** Concrete time savings for a real-world Windows image.

## Phase 3: Proposal writeup

**Goal:** Internal document to propose the initiative.

**Contents:**
- Problem: Windows container startup is slow due to large image pulls
- Solution: Extend SOCI to Windows via cgofuse/WinFSP
- Evidence: PoC results (Phase 0-2)
- Architecture: dependency mapping, interface refactor plan, WCOW integration plan
- Alternatives: CimFS as potential native path
- Scope estimate: engineering effort for full implementation
- Risks: CimFS might supersede WinFSP approach, WCOW integration complexity

## Phase 4 (post-proposal): Full implementation

Only if proposal is approved:
- Refactor Layer interface to decouple from go-fuse
- Implement cgofuse node layer using SOCI reader
- Build Windows containerd snapshotter (WCOW integration)
- Investigate CimFS as complement/alternative
- CI: add Windows build + test targets
