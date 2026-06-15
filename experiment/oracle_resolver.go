/*
   Copyright The Soci Snapshotter Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package experiment

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	commonmetrics "github.com/awslabs/soci-snapshotter/fs/metrics/common"
	sm "github.com/awslabs/soci-snapshotter/fs/span-manager"
	"github.com/awslabs/soci-snapshotter/ztoc/compression"
	"github.com/opencontainers/go-digest"
	"golang.org/x/net/context"
)

// OracleResolver is a backgroundfetcher.Resolver that fetches spans in a
// pre-recorded access order, then falls back to sequential for remaining spans.
type OracleResolver struct {
	spanManager  *sm.SpanManager
	layerDigest  digest.Digest
	order        []compression.SpanID
	idx          int
	maxSpanID    compression.SpanID
	sequentialID compression.SpanID
	inSequential bool
	closed       bool
	closedMu     sync.Mutex
	start        time.Time
}

// LoadAccessOrder reads an access log file and returns the span IDs for the given layer.
func LoadAccessOrder(path string, layerDigest digest.Digest) ([]compression.SpanID, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var order []compression.SpanID
	seen := make(map[compression.SpanID]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var layer string
		var spanID compression.SpanID
		if _, err := fmt.Sscanf(scanner.Text(), "%s %d", &layer, &spanID); err != nil {
			continue
		}
		if layer != layerDigest.String() {
			continue
		}
		if !seen[spanID] {
			order = append(order, spanID)
			seen[spanID] = true
		}
	}
	return order, scanner.Err()
}

func NewOracleResolver(layerDigest digest.Digest, spanManager *sm.SpanManager, order []compression.SpanID, maxSpanID compression.SpanID) *OracleResolver {
	return &OracleResolver{
		spanManager: spanManager,
		layerDigest: layerDigest,
		order:       order,
		maxSpanID:   maxSpanID,
	}
}

func (r *OracleResolver) Resolve(_ context.Context) (bool, error) {
	if r.idx == 0 && !r.inSequential {
		r.start = time.Now()
	}

	var spanID compression.SpanID

	if !r.inSequential && r.idx < len(r.order) {
		// Phase 1: fetch in recorded access order
		spanID = r.order[r.idx]
		r.idx++
	} else {
		// Phase 2: sequential fallback for remaining spans
		if !r.inSequential {
			r.inSequential = true
			r.sequentialID = 0
		}
		// Skip spans already fetched in phase 1
		for r.sequentialID <= r.maxSpanID {
			spanID = r.sequentialID
			r.sequentialID++
			// FetchSingleSpan skips non-unrequested spans internally, so just try it
			break
		}
		if r.sequentialID > r.maxSpanID+1 {
			commonmetrics.MeasureLatencyInMilliseconds(commonmetrics.BackgroundFetch, r.layerDigest, r.start)
			return false, nil
		}
	}

	err := r.spanManager.FetchSingleSpan(spanID)
	if err == nil {
		commonmetrics.IncOperationCount(commonmetrics.BackgroundSpanFetchCount, r.layerDigest)
	} else if errors.Is(err, sm.ErrExceedMaxSpan) {
		commonmetrics.MeasureLatencyInMilliseconds(commonmetrics.BackgroundFetch, r.layerDigest, r.start)
		return false, nil
	} else {
		commonmetrics.IncOperationCount(commonmetrics.BackgroundSpanFetchFailureCount, r.layerDigest)
		return false, err
	}

	// More work if still in oracle phase, or sequential hasn't finished
	if !r.inSequential {
		return true, nil
	}
	return r.sequentialID <= r.maxSpanID, nil
}

func (r *OracleResolver) Close() error {
	r.closedMu.Lock()
	defer r.closedMu.Unlock()
	r.closed = true
	return nil
}

func (r *OracleResolver) Closed() bool {
	r.closedMu.Lock()
	defer r.closedMu.Unlock()
	return r.closed
}
