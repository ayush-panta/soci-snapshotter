# SOCI on Windows

## Summary

Port SOCI's FUSE-based lazy loading to Windows using WinFSP as the filesystem driver. Enables lazy loading for Windows container images.

## Key Considerations

- WinFSP provides FUSE-compatible API on Windows (local clone at `../winfsp/`)
- Need to abstract FUSE layer behind an interface (Linux FUSE vs. WinFSP)
- containerd on Windows uses different snapshotter model (wcow)
- Registry interaction should be platform-agnostic already
- Scope: start with read-only mount path, skip write layer initially

## Status

Triage
