package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// RegistryClient handles Docker Registry HTTP API v2 interactions with ECR.
type RegistryClient struct {
	baseURL    string // e.g., "https://299170649678.dkr.ecr.us-west-2.amazonaws.com"
	repo       string // e.g., "soci-on-windows"
	authHeader string // "Basic <base64>" from ECR login
	httpClient *http.Client
}

// NewRegistryClient creates a registry client for ECR.
func NewRegistryClient(registryURL, repo, authToken string) *RegistryClient {
	return &RegistryClient{
		baseURL:    registryURL,
		repo:       repo,
		authHeader: "Basic " + authToken,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// OCIManifest is a minimal OCI image manifest.
type OCIManifest struct {
	MediaType string            `json:"mediaType"`
	Config    OCIDescriptor     `json:"config"`
	Layers    []OCIDescriptor   `json:"layers"`
}

// OCIIndex is a minimal OCI index (referrers response or image index).
type OCIIndex struct {
	Manifests []OCIDescriptor `json:"manifests"`
}

// OCIDescriptor is a content descriptor.
type OCIDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
	ArtifactType string           `json:"artifactType,omitempty"`
}

// FetchManifest fetches and parses the image manifest for a given reference (tag or digest).
// If the reference resolves to a manifest list, it picks the first windows/amd64 manifest.
func (rc *RegistryClient) FetchManifest(ref string) (*OCIManifest, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", rc.baseURL, rc.repo, ref)
	data, err := rc.doGet(url, "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json")
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}

	// Check if this is a manifest list/index
	var probe struct {
		MediaType string          `json:"mediaType"`
		Manifests []OCIDescriptor `json:"manifests"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && len(probe.Manifests) > 0 {
		// It's a manifest list — pick the first manifest (should be windows/amd64)
		log.Printf("[registry] Manifest list with %d entries, resolving first platform manifest: %s", len(probe.Manifests), probe.Manifests[0].Digest)
		return rc.FetchManifest(probe.Manifests[0].Digest)
	}

	var m OCIManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// FetchReferrers fetches OCI referrers for a given manifest digest.
// This is how we find the SOCI index artifact.
func (rc *RegistryClient) FetchReferrers(digest string) (*OCIIndex, error) {
	url := fmt.Sprintf("%s/v2/%s/referrers/%s", rc.baseURL, rc.repo, digest)
	data, err := rc.doGet(url, "application/vnd.oci.image.index.v1+json")
	if err != nil {
		return nil, fmt.Errorf("fetch referrers: %w", err)
	}
	var idx OCIIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse referrers: %w", err)
	}
	return &idx, nil
}

// FetchSOCIIndex fetches and parses the SOCI index manifest.
func (rc *RegistryClient) FetchSOCIIndex(sociIndexDigest string) (*OCIManifest, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", rc.baseURL, rc.repo, sociIndexDigest)
	data, err := rc.doGet(url, "application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		return nil, fmt.Errorf("fetch SOCI index: %w", err)
	}
	var m OCIManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse SOCI index: %w", err)
	}
	return &m, nil
}

// FetchBlob downloads a blob by digest.
func (rc *RegistryClient) FetchBlob(digest string) ([]byte, error) {
	url := fmt.Sprintf("%s/v2/%s/blobs/%s", rc.baseURL, rc.repo, digest)
	data, err := rc.doGet(url, "")
	if err != nil {
		return nil, fmt.Errorf("fetch blob %s: %w", digest, err)
	}
	return data, nil
}

// BlobURL returns the URL for a blob (for Range requests).
func (rc *RegistryClient) BlobURL(digest string) string {
	return fmt.Sprintf("%s/v2/%s/blobs/%s", rc.baseURL, rc.repo, digest)
}

// AuthHeader returns the auth header for use by SpanFetcher.
func (rc *RegistryClient) AuthHeader() string {
	return rc.authHeader
}

func (rc *RegistryClient) doGet(url, accept string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", rc.authHeader)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	log.Printf("[registry] GET %s", url)
	resp, err := rc.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	return io.ReadAll(resp.Body)
}
