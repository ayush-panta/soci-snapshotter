package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/awslabs/soci-snapshotter/cache"
	"github.com/awslabs/soci-snapshotter/fs/reader"
	spanmanager "github.com/awslabs/soci-snapshotter/fs/span-manager"
	"github.com/awslabs/soci-snapshotter/metadata"
	"github.com/awslabs/soci-snapshotter/ztoc"
	"github.com/opencontainers/go-digest"
	"github.com/winfsp/cgofuse/fuse"
)

const (
	defaultRegistry = "https://299170649678.dkr.ecr.us-west-2.amazonaws.com"
	defaultRepo     = "soci-on-windows"
	defaultTag      = "datadog-ltsc22"
	// SOCI index media type
	sociIndexArtifactType = "application/vnd.amazon.soci.index.v1+json"
)

func main() {
	mountPoint := flag.String("mount", "W:", "Mount point")
	registry := flag.String("registry", defaultRegistry, "Registry URL")
	repo := flag.String("repo", defaultRepo, "Repository name")
	tag := flag.String("tag", defaultTag, "Image tag")
	layerIndex := flag.Int("layer", -1, "Layer index (0-based, -1 = largest)")
	cacheDir := flag.String("cache-dir", "C:\\dev\\soci-cache", "Local span cache directory")
	flag.Parse()

	ctx := context.Background()
	_ = ctx

	// Step 1: ECR auth
	log.Println("Getting ECR auth token...")
	authToken, err := getECRToken()
	if err != nil {
		log.Fatalf("Failed to get ECR token: %v", err)
	}
	log.Println("Got ECR auth token")

	// Step 2: Registry client
	client := NewRegistryClient(*registry, *repo, authToken)

	// Step 3: Resolve image manifest
	log.Printf("Fetching image manifest for %s:%s...", *repo, *tag)
	manifest, err := client.FetchManifest(*tag)
	if err != nil {
		log.Fatalf("Failed to fetch manifest: %v", err)
	}
	log.Printf("Image has %d layers", len(manifest.Layers))

	// Step 4: Pick target layer
	targetLayer := pickLayer(manifest.Layers, *layerIndex)
	log.Printf("Target layer: %s (size: %d bytes)", targetLayer.Digest, targetLayer.Size)

	// Step 5: Find SOCI index
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
		log.Fatalf("No SOCI index found for %s", imageDigest)
	}
	log.Printf("Found SOCI index: %s", sociIndexDesc.Digest)

	// Step 6: Fetch SOCI index → find zTOC
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

	// Step 8: Create remote blob reader (SectionReader over HTTP Range)
	blobURL := client.BlobURL(targetLayer.Digest)
	blobReaderAt := &httpReaderAt{
		url:        blobURL,
		authHeader: client.AuthHeader(),
		client:     &http.Client{Timeout: 60 * time.Second},
	}
	sr := io.NewSectionReader(blobReaderAt, 0, targetLayer.Size)

	// Step 9: Create SOCI metadata reader
	log.Println("Initializing metadata store...")
	metaReader, err := metadata.NewTempDbStore(sr, z.TOC)
	if err != nil {
		log.Fatalf("Failed to create metadata reader: %v", err)
	}
	defer metaReader.Close()
	log.Println("Metadata store ready")

	// Step 10: Create span cache
	os.MkdirAll(*cacheDir, 0755)
	spanCache, err := cache.NewDirectoryCache(*cacheDir, cache.DirectoryCacheConfig{
		SyncAdd:   true,
		MaxLRUCacheEntry: 100,
		MaxCacheFds:      10,
	})
	if err != nil {
		log.Fatalf("Failed to create span cache: %v", err)
	}
	defer spanCache.Close()
	log.Println("Span cache ready at", *cacheDir)

	// Step 11: Create span manager (SOCI's real implementation)
	layerDigest, err := digest.Parse(targetLayer.Digest)
	if err != nil {
		log.Fatalf("Failed to parse layer digest: %v", err)
	}

	spanMgr, err := spanmanager.New(z, sr, spanCache, 3, layerDigest, cache.Direct())
	if err != nil {
		log.Fatalf("Failed to create span manager: %v", err)
	}
	log.Printf("Span manager ready (%d spans)", z.MaxSpanID+1)

	// Step 12: Create SOCI reader
	rdr, err := reader.NewReader(metaReader, layerDigest, spanMgr, true /* disable verification for PoC */)
	if err != nil {
		log.Fatalf("Failed to create reader: %v", err)
	}
	defer rdr.Close()
	log.Println("SOCI reader ready")

	// Step 13: Mount via cgofuse
	fs := NewSOCILayerFS(metaReader, rdr)
	host := fuse.NewFileSystemHost(fs)
	log.Printf("Mounting at %s ...", *mountPoint)
	log.Println("Press Ctrl+C to unmount and exit")
	log.Println("Second reads should be instant (cached)")

	ok := host.Mount(*mountPoint, []string{"-o", "ro", "-o", "uid=-1", "-o", "gid=-1"})
	if !ok {
		log.Fatal("Mount failed")
	}
}

// httpReaderAt implements io.ReaderAt over HTTP Range requests.
type httpReaderAt struct {
	url        string
	authHeader string
	client     *http.Client
}

func (h *httpReaderAt) ReadAt(p []byte, off int64) (int, error) {
	req, err := http.NewRequest("GET", h.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
	if h.authHeader != "" {
		req.Header.Set("Authorization", h.authHeader)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d for range %d-%d", resp.StatusCode, off, off+int64(len(p))-1)
	}

	return io.ReadFull(resp.Body, p)
}

// getECRToken runs `aws ecr get-login-password` and returns the base64 auth token.
func getECRToken() (string, error) {
	cmd := exec.Command("aws", "ecr", "get-login-password", "--region", "us-west-2")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("aws ecr get-login-password failed: %w", err)
	}
	password := strings.TrimSpace(string(out))
	token := base64.StdEncoding.EncodeToString([]byte("AWS:" + password))
	return token, nil
}

// getManifestDigest resolves through manifest lists to get the platform manifest digest.
func getManifestDigest(client *RegistryClient, tag string) (string, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", client.baseURL, client.repo, tag)

	data, err := client.doGet(url, "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json")
	if err != nil {
		return "", err
	}

	var probe struct {
		Manifests []OCIDescriptor `json:"manifests"`
	}
	if err := jsonUnmarshal(data, &probe); err == nil && len(probe.Manifests) > 0 {
		platformDigest := probe.Manifests[0].Digest
		log.Printf("Manifest list resolved to platform manifest: %s", platformDigest)
		return platformDigest, nil
	}

	// Single manifest - do HEAD to get digest
	req, _ := http.NewRequest("HEAD", url, nil)
	req.Header.Set("Authorization", client.authHeader)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", fmt.Errorf("no Docker-Content-Digest header")
	}
	return d, nil
}

func pickLayer(layers []OCIDescriptor, index int) OCIDescriptor {
	if index >= 0 && index < len(layers) {
		return layers[index]
	}
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
	for i, l := range sociIndex.Layers {
		if ann := l.Annotations; ann != nil {
			if ann["com.amazon.soci.image-layer-digest"] == layerDigest {
				return &sociIndex.Layers[i]
			}
		}
	}
	return nil
}

func init() {
	logFile, err := os.OpenFile(filepath.Join("C:\\dev", "phase2-soci.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	}
}
