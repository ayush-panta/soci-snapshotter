package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/awslabs/soci-snapshotter/ztoc/compression"
)

// SpanFetcher fetches and decompresses spans from a remote compressed layer blob.
type SpanFetcher struct {
	blobURL               string
	authHeader            string // "Basic <token>" for ECR
	httpClient            *http.Client
	zinfo                 compression.Zinfo
	compressedArchiveSize int64
	mu                    sync.Mutex
}

// NewSpanFetcher creates a SpanFetcher for the given layer blob.
func NewSpanFetcher(blobURL, authHeader string, zinfo compression.Zinfo, compressedSize int64) *SpanFetcher {
	return &SpanFetcher{
		blobURL:               blobURL,
		authHeader:            authHeader,
		httpClient:            &http.Client{Timeout: 30 * time.Second},
		zinfo:                 zinfo,
		compressedArchiveSize: compressedSize,
	}
}

// ReadFile reads `len(buf)` bytes starting at `readOffset` within a file whose
// uncompressed content starts at `uncompOffset` with total size `uncompSize` in
// the tar stream.
func (sf *SpanFetcher) ReadFile(uncompOffset, uncompSize int64, buf []byte, readOffset int64) (int, error) {
	if readOffset >= uncompSize {
		return 0, io.EOF
	}

	// Calculate how much to actually read
	toRead := int64(len(buf))
	if readOffset+toRead > uncompSize {
		toRead = uncompSize - readOffset
	}

	// The absolute offset in the uncompressed tar stream
	absStart := uncompOffset + readOffset
	absEnd := absStart + toRead

	// Find which spans we need
	spanStart := sf.zinfo.UncompressedOffsetToSpanID(compression.Offset(absStart))
	spanEnd := sf.zinfo.UncompressedOffsetToSpanID(compression.Offset(absEnd - 1))
	numSpans := spanEnd - spanStart + 1

	// Calculate compressed byte range to fetch
	compStart := sf.zinfo.StartCompressedOffset(spanStart)
	var compEnd compression.Offset
	if spanEnd == sf.zinfo.MaxSpanID() {
		compEnd = compression.Offset(sf.compressedArchiveSize)
	} else {
		compEnd = sf.zinfo.EndCompressedOffset(spanEnd, compression.Offset(sf.compressedArchiveSize))
	}

	log.Printf("[spanfetch] file offset=%d, spans %d-%d (%d spans), compressed range %d-%d (%d bytes)",
		readOffset, spanStart, spanEnd, numSpans, compStart, compEnd, compEnd-compStart)

	// Fetch compressed bytes via HTTP Range
	compressedBuf, err := sf.fetchRange(int64(compStart), int64(compEnd))
	if err != nil {
		return 0, fmt.Errorf("fetch range failed: %w", err)
	}

	// Decompress
	sf.mu.Lock()
	decompressed, err := sf.zinfo.ExtractDataFromBuffer(
		compressedBuf,
		compression.Offset(toRead),
		compression.Offset(absStart),
		spanStart,
	)
	sf.mu.Unlock()
	if err != nil {
		return 0, fmt.Errorf("decompress failed: %w", err)
	}

	n := copy(buf, decompressed)
	if int64(n) < toRead {
		return n, io.EOF
	}
	return n, nil
}

// fetchRange does an HTTP Range GET for bytes [start, end).
func (sf *SpanFetcher) fetchRange(start, end int64) ([]byte, error) {
	req, err := http.NewRequest("GET", sf.blobURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
	if sf.authHeader != "" {
		req.Header.Set("Authorization", sf.authHeader)
	}

	resp, err := sf.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d for range %d-%d", resp.StatusCode, start, end)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	return data, nil
}
