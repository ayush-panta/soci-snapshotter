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
	"fmt"
	"os"
	"sync"

	spanmanager "github.com/awslabs/soci-snapshotter/fs/span-manager"
	"github.com/awslabs/soci-snapshotter/ztoc/compression"
	"github.com/opencontainers/go-digest"
)

// Ensure FileAccessLogger implements spanmanager.AccessLogger.
var _ spanmanager.AccessLogger = (*FileAccessLogger)(nil)

// FileAccessLogger writes on-demand span accesses to a file.
// Format: one line per access: "<layer_digest> <span_id>"
type FileAccessLogger struct {
	mu   sync.Mutex
	file *os.File
}

func NewFileAccessLogger(path string) (*FileAccessLogger, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create access log: %w", err)
	}
	return &FileAccessLogger{file: f}, nil
}

func (l *FileAccessLogger) LogAccess(layerSha digest.Digest, spanID compression.SpanID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.file, "%s %d\n", layerSha.String(), spanID)
}

func (l *FileAccessLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
