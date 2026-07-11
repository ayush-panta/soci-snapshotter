package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/awslabs/soci-snapshotter/ztoc"
	"github.com/awslabs/soci-snapshotter/ztoc/compression"
	"github.com/winfsp/cgofuse/fuse"
)

const (
	defaultRegistry = "https://299170649678.dkr.ecr.us-west-2.amazonaws.com"
	defaultRepo     = "soci-on-windows"
	defaultTag      = "datadog-ltsc22"
	// SOCI index media type
	sociIndexArtifactType = "application/vnd.amazon.soci.index.v1+json"
	// zTOC media type
	ztocMediaType = "application/octet-stream"
)

func main() {
	mountPoint := flag.String("mount", "W:", "Mount point (e.g., W: on Windows, /mnt/layer on Linux)")
	registry := flag.String("registry", defaultRegistry, "Registry URL")
	repo := flag.String("repo", defaultRepo, "Repository name")
	tag := flag.String("tag", defaultTag, "Image tag")
	layerIndex := flag.Int("layer", -1, "Layer index to mount (0-based, -1 = largest)")
	flag.Parse()

	// Step 1: Get ECR auth token
	log.Println("Getting ECR auth token...")
	authToken, err := getECRToken()
	if err != nil {
		log.Fatalf("Failed to get ECR token: %v", err)
	}
	log.Println("Got ECR auth token")

	// Step 2: Create registry client
	client := NewRegistryClient(*registry, *repo, authToken)

	// Step 3: Fetch image manifest
	log.Printf("Fetching image manifest for %s:%s...", *repo, *tag)
	manifest, err := client.FetchManifest(*tag)
	if err != nil {
		log.Fatalf("Failed to fetch manifest: %v", err)
	}
	log.Printf("Image has %d layers", len(manifest.Layers))

	// Step 4: Pick target layer
	targetLayer := pickLayer(manifest.Layers, *layerIndex)
	log.Printf("Target layer: %s (size: %d bytes)", targetLayer.Digest, targetLayer.Size)

	// Step 5: Find SOCI index via referrers API
	log.Println("Looking up SOCI index via referrers API...")
	imageDigest, err := getManifestDigest(client, *tag)
	if err != nil {
		log.Fatalf("Failed to get manifest digest: %v", err)
	}

	referrers, err := client.FetchReferrers(imageDigest)
	if err != nil {
		log.Fatalf("Failed to fetch referrers: %v", err)
	}

	sociIndexDesc := findSOCIIndex(referrers)
	if sociIndexDesc == nil {
		log.Fatalf("No SOCI index found in referrers for %s", imageDigest)
	}
	log.Printf("Found SOCI index: %s", sociIndexDesc.Digest)

	// Step 6: Fetch SOCI index manifest → find zTOC for our layer
	sociIndex, err := client.FetchSOCIIndex(sociIndexDesc.Digest)
	if err != nil {
		log.Fatalf("Failed to fetch SOCI index manifest: %v", err)
	}

	ztocDesc := findZtocForLayer(sociIndex, targetLayer.Digest)
	if ztocDesc == nil {
		log.Fatalf("No zTOC found for layer %s", targetLayer.Digest)
	}
	log.Printf("Found zTOC: %s (size: %d)", ztocDesc.Digest, ztocDesc.Size)

	// Step 7: Fetch and parse zTOC
	log.Println("Fetching zTOC...")
	ztocBlob, err := client.FetchBlob(ztocDesc.Digest)
	if err != nil {
		log.Fatalf("Failed to fetch zTOC blob: %v", err)
	}

	z, err := ztoc.Unmarshal(bytes.NewReader(ztocBlob))
	if err != nil {
		log.Fatalf("Failed to unmarshal zTOC: %v", err)
	}
	log.Printf("zTOC parsed: %d files, %d spans, compressed size %d",
		len(z.TOC.FileMetadata), z.MaxSpanID+1, z.CompressedArchiveSize)

	// Step 8: Build file tree
	log.Println("Building file tree...")
	tree := BuildFileTree(z)
	fileCount, dirCount := countNodes(tree.Root)
	log.Printf("File tree: %d files, %d directories", fileCount, dirCount)

	// Step 9: Create zinfo for decompression
	compressionAlgo := z.CompressionAlgorithm
	if compressionAlgo == "" {
		compressionAlgo = compression.Gzip
	}
	zinfo, err := compression.NewZinfo(compressionAlgo, z.Checkpoints)
	if err != nil {
		log.Fatalf("Failed to create zinfo: %v", err)
	}
	defer zinfo.Close()

	// Step 10: Create span fetcher
	blobURL := client.BlobURL(targetLayer.Digest)
	fetcher := NewSpanFetcher(blobURL, client.AuthHeader(), zinfo, int64(z.CompressedArchiveSize))

	// Step 11: Create and mount FUSE filesystem
	fs := NewLayerFS(tree, fetcher)
	host := fuse.NewFileSystemHost(fs)
	log.Printf("Mounting at %s ...", *mountPoint)
	log.Println("Press Ctrl+C to unmount and exit")

	// Mount (blocking)
	ok := host.Mount(*mountPoint, []string{"-o", "ro", "-o", "uid=-1", "-o", "gid=-1"})
	if !ok {
		log.Fatal("Mount failed")
	}
}

// getECRToken runs `aws ecr get-login-password` and returns the base64 auth token.
func getECRToken() (string, error) {
	cmd := exec.Command("aws", "ecr", "get-login-password", "--region", "us-west-2")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("aws ecr get-login-password failed: %w", err)
	}
	password := strings.TrimSpace(string(out))
	// ECR auth is Basic base64("AWS:<password>")
	token := base64.StdEncoding.EncodeToString([]byte("AWS:" + password))
	return token, nil
}

// getManifestDigest fetches the manifest by tag and returns its digest.
// If the tag points to a manifest list, it resolves to the first platform manifest digest.
func getManifestDigest(client *RegistryClient, tag string) (string, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", client.baseURL, client.repo, tag)

	// First try HEAD to get the manifest list digest
	req, _ := http.NewRequest("HEAD", url, nil)
	req.Header.Set("Authorization", client.authHeader)
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.index.v1+json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()

	// Now fetch the actual content to check if it's a list
	data, err := client.doGet(url, "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json")
	if err != nil {
		return "", err
	}

	var probe struct {
		Manifests []OCIDescriptor `json:"manifests"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && len(probe.Manifests) > 0 {
		// It's a manifest list — the SOCI index is attached to the platform manifest
		platformDigest := probe.Manifests[0].Digest
		log.Printf("Manifest list resolved to platform manifest: %s", platformDigest)
		return platformDigest, nil
	}

	// It's a single manifest — use the Docker-Content-Digest from HEAD
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("no Docker-Content-Digest header in manifest response")
	}
	return digest, nil
}

func pickLayer(layers []OCIDescriptor, index int) OCIDescriptor {
	if index >= 0 && index < len(layers) {
		return layers[index]
	}
	// Pick largest layer
	largest := 0
	for i, l := range layers {
		if l.Size > layers[largest].Size {
			largest = i
		}
	}
	return layers[largest]
}

func findSOCIIndex(referrers *OCIIndex) *OCIDescriptor {
	for i, m := range referrers.Manifests {
		if m.ArtifactType == sociIndexArtifactType {
			return &referrers.Manifests[i]
		}
	}
	return nil
}

func findZtocForLayer(sociIndex *OCIManifest, layerDigest string) *OCIDescriptor {
	// In a SOCI index, layers in the manifest correspond to zTOCs for each image layer.
	// The annotation "com.amazon.soci.image-layer-digest" maps zTOC → layer.
	for i, l := range sociIndex.Layers {
		ann := l.Annotations
		if ann != nil {
			if ann["com.amazon.soci.image-layer-digest"] == layerDigest {
				return &sociIndex.Layers[i]
			}
		}
	}
	// Fallback: check mediaType for ztoc
	for i, l := range sociIndex.Layers {
		if l.MediaType == ztocMediaType || l.MediaType == "application/vnd.amazon.soci.ztoc.v1" {
			// If only one zTOC, use it
			if len(sociIndex.Layers) == 1 {
				return &sociIndex.Layers[i]
			}
		}
	}
	return nil
}

func countNodes(node *FileNode) (files, dirs int) {
	if node.IsDir {
		dirs++
		for _, child := range node.Children {
			f, d := countNodes(child)
			files += f
			dirs += d
		}
	} else {
		files++
	}
	return
}

func init() {
	// Ensure we can find aws CLI
	if _, err := exec.LookPath("aws"); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: 'aws' CLI not found in PATH")
	}
}
