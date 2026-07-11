# Phase 1: Real Layer via WinFSP

Serves a real Windows container layer from ECR lazily through WinFSP/cgofuse.

## Prerequisites (Windows instance)

- Go 1.23+ with CGO_ENABLED=1
- MinGW-w64 (`gcc` on PATH)
- WinFSP installed (https://winfsp.dev)
- AWS CLI configured with access to `299170649678` ECR
- cgofuse (`C:\dev\cgofuse` or wherever it was set up)

## Build

```powershell
# From C:\dev\soci-contrib\poc\phase1-real-layer
$env:CGO_ENABLED = "1"
$env:CC = "gcc"
go build -o phase1.exe .
```

## Run

```powershell
# Mount the largest layer from the Datadog image at W:\
.\phase1.exe -mount W:

# Mount a specific layer (0-based index)
.\phase1.exe -mount W: -layer 3

# Custom image
.\phase1.exe -mount W: -registry https://299170649678.dkr.ecr.us-west-2.amazonaws.com -repo soci-on-windows -tag datadog-ltsc22
```

## Verify

```powershell
# List top-level directories
dir W:\

# Read a file
type W:\path\to\some\file.txt

# Measure cold read time
Measure-Command { Get-Content W:\Windows\System32\ntdll.dll | Out-Null }
```

## How it works

1. Gets ECR auth token via `aws ecr get-login-password`
2. Fetches image manifest → finds the target layer digest
3. Finds the SOCI index via the OCI referrers API
4. Downloads the zTOC artifact for the target layer
5. Parses zTOC → builds in-memory file tree
6. Mounts a WinFSP filesystem at `W:\`
7. On each `Read` call: fetches compressed spans via HTTP Range GET → decompresses with zlib (SOCI's compression package) → returns bytes

## Troubleshooting

- **Mount fails**: Ensure WinFSP is installed and you have admin rights
- **AWS auth fails**: Run `aws configure` or set `AWS_PROFILE=299170649678`
- **ECR 401**: Re-run and ensure the AWS CLI is in PATH
- **No SOCI index found**: The referrers API requires ECR and the image must have a SOCI index pushed
