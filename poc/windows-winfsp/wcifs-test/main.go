package main

import (
	"fmt"
	"os"

	"github.com/Microsoft/hcsshim"
)

// This test checks if WCIFS/PrepareLayer can work against a WinFSP-mounted directory.
// If this works, SOCI lazy-loaded layers can be composed by the standard Windows container runtime.
func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: wcifs-test.exe <layer-path>")
		fmt.Println("Example: wcifs-test.exe W:\\Files")
		fmt.Println("")
		fmt.Println("This tests if hcsshim.PrepareLayer can use a WinFSP mount as a container layer.")
		os.Exit(1)
	}

	layerPath := os.Args[1]
	fmt.Printf("Testing layer path: %s\n", layerPath)

	// Check the path exists
	if _, err := os.Stat(layerPath); err != nil {
		fmt.Printf("ERROR: Path does not exist: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Path exists: OK")

	// DriverInfo points to the parent of the layer dir
	di := hcsshim.DriverInfo{
		HomeDir: layerPath,
	}

	// For a base layer (no parents), PrepareLayer needs no parent paths.
	// Our mounted layer IS a base layer (the servercore layer).
	parentLayerPaths := []string{}

	fmt.Println("Calling hcsshim.ActivateLayer...")
	err := hcsshim.ActivateLayer(di, ".")
	if err != nil {
		fmt.Printf("ActivateLayer FAILED: %v\n", err)
		fmt.Println("")
		fmt.Println("This means WCIFS cannot directly use a WinFSP mount.")
		fmt.Println("We would need Option B: single unified WinFSP mount with union logic.")
		os.Exit(1)
	}
	fmt.Println("ActivateLayer: OK")

	fmt.Println("Calling hcsshim.PrepareLayer...")
	err = hcsshim.PrepareLayer(di, ".", parentLayerPaths)
	if err != nil {
		fmt.Printf("PrepareLayer FAILED: %v\n", err)
		hcsshim.DeactivateLayer(di, ".")
		fmt.Println("")
		fmt.Println("ActivateLayer worked but PrepareLayer failed.")
		fmt.Println("WCIFS may not be able to compose through WinFSP.")
		os.Exit(1)
	}
	fmt.Println("PrepareLayer: OK")

	// Try to get the mount path (this is what the container would see)
	fmt.Println("Calling hcsshim.GetLayerMountPath...")
	mountPath, err := hcsshim.GetLayerMountPath(di, ".")
	if err != nil {
		fmt.Printf("GetLayerMountPath FAILED: %v\n", err)
	} else {
		fmt.Printf("GetLayerMountPath: %s\n", mountPath)
	}

	// Cleanup
	hcsshim.UnprepareLayer(di, ".")
	hcsshim.DeactivateLayer(di, ".")

	fmt.Println("")
	fmt.Println("SUCCESS: WCIFS can use a WinFSP-mounted directory as a layer!")
	fmt.Println("This means SOCI lazy-loaded layers can integrate with the standard container runtime.")
}
