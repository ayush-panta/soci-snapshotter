# Windows Dev Instance Setup

## AWS Details

- **Account:** 299170649678
- **Region:** us-west-2

## Instance Spec

| Setting | Value |
|---------|-------|
| AMI | Windows Server 2022 Full Base (latest) |
| Instance type | m5.xlarge |
| Storage | 100 GB gp3 |
| Key pair | None needed (using SSM) |
| Security group | No inbound rules required |
| IAM role | Attach role with `AmazonSSMManagedInstanceCore` + ECR read access |

## Connect via SSM

```bash
aws ssm start-session --target <instance-id> --region us-west-2
```

This drops you into a PowerShell session. No RDP, no GUI, no inbound ports.

## Software to Install (in order)

### 1. Git

```powershell
winget install Git.Git
```

### 2. Go

Install Go 1.21+ (match soci-snapshotter's go.mod).

```powershell
winget install GoLang.Go
```

Verify: `go version`

### 3. Visual Studio Build Tools

Needed for CGo (WinFSP C API bindings).

```powershell
winget install Microsoft.VisualStudio.2022.BuildTools --override "--add Microsoft.VisualStudio.Workload.VCTools --includeRecommended --passive"
```

### 4. WinFSP

Download and install from https://winfsp.dev/rel/

- Use the MSI installer
- Select "Developer" option during install (includes SDK headers + libs)
- Default install path: `C:\Program Files (x86)\WinFsp`
- Add to PATH: `C:\Program Files (x86)\WinFsp\bin`

Verify driver loaded: `sc query WinFsp.Launcher`

### 5. AWS CLI

```powershell
winget install Amazon.AWSCLI
```

Configure with credentials that have ECR read access.

### 6. Windows Containers Feature

```powershell
Install-WindowsFeature -Name Containers
Restart-Computer
```

### 7. containerd (for later — not needed for initial PoC)

```powershell
# Download latest containerd Windows release
# https://github.com/containerd/containerd/releases
# Extract to C:\containerd and add to PATH
```

## Clone Repos

```powershell
mkdir C:\dev
cd C:\dev
git clone https://github.com/awslabs/soci-snapshotter.git
git clone https://github.com/winfsp/winfsp.git
```

## Pre-generate Test Data (on Linux first)

Before using the Windows instance, generate a zTOC on Linux and note:
- The layer digest (sha256:...)
- The zTOC file (copy to Windows instance)
- The registry + repository where the layer lives

This avoids needing zTOC creation tooling on Windows for the initial PoC.

## Verify Setup

Quick smoke test that Go + WinFSP SDK are wired:

```go
// save as C:\dev\winfsp-check\main.go
package main

import "fmt"

/*
#cgo CFLAGS: -I"C:/Program Files (x86)/WinFsp/inc"
#cgo LDFLAGS: -L"C:/Program Files (x86)/WinFsp/lib" -lwinfsp-x64
#include <winfsp/winfsp.h>
*/
import "C"

func main() {
	fmt.Println("WinFSP version:", C.FSP_FSCTL_PRODUCT_NAME)
}
```

```powershell
cd C:\dev\winfsp-check
go build .
```

If this compiles, CGo + WinFSP SDK are linked correctly.

## Next Steps

1. Write a minimal WinFSP filesystem in Go that serves hardcoded files
2. Wire it to soci-snapshotter's `reader.Reader` with a pre-built zTOC
3. Benchmark: measure read latency vs native file reads
