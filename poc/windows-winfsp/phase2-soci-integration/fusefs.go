package main

import (
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/awslabs/soci-snapshotter/fs/reader"
	"github.com/awslabs/soci-snapshotter/metadata"
	"github.com/winfsp/cgofuse/fuse"
)

// SOCILayerFS implements cgofuse filesystem backed by SOCI's metadata.Reader and reader.Reader.
type SOCILayerFS struct {
	fuse.FileSystemBase
	meta metadata.Reader
	rdr  reader.Reader
}

// NewSOCILayerFS creates a FUSE filesystem backed by SOCI's real reader pipeline.
func NewSOCILayerFS(meta metadata.Reader, rdr reader.Reader) *SOCILayerFS {
	return &SOCILayerFS{
		meta: meta,
		rdr:  rdr,
	}
}

func (fs *SOCILayerFS) Getattr(path string, stat *fuse.Stat_t, fh uint64) int {
	id, attr, err := fs.lookupPath(path)
	if err != nil {
		return -fuse.ENOENT
	}
	_ = id
	fillStatFromAttr(attr, stat)
	return 0
}

func (fs *SOCILayerFS) Readdir(path string,
	fill func(name string, stat *fuse.Stat_t, ofst int64) bool,
	ofst int64, fh uint64) int {

	id, attr, err := fs.lookupPath(path)
	if err != nil {
		return -fuse.ENOENT
	}
	if !attr.Mode.IsDir() {
		return -fuse.ENOTDIR
	}

	fill(".", nil, 0)
	fill("..", nil, 0)

	fs.meta.ForeachChild(id, func(name string, childID uint32, mode os.FileMode) bool {
		childAttr, err := fs.meta.GetAttr(childID)
		if err != nil {
			return true // skip this child
		}
		var st fuse.Stat_t
		fillStatFromAttr(childAttr, &st)
		return fill(name, &st, 0)
	})

	return 0
}

func (fs *SOCILayerFS) Open(path string, flags int) (int, uint64) {
	_, attr, err := fs.lookupPath(path)
	if err != nil {
		return -fuse.ENOENT, 0
	}
	if attr.Mode.IsDir() {
		return -fuse.EISDIR, 0
	}
	return 0, 0
}

func (fs *SOCILayerFS) Read(path string, buff []byte, ofst int64, fh uint64) int {
	id, attr, err := fs.lookupPath(path)
	if err != nil {
		return -fuse.ENOENT
	}
	if attr.Mode.IsDir() {
		return -fuse.EISDIR
	}
	if attr.Size == 0 {
		return 0
	}

	start := time.Now()

	ra, err := fs.rdr.OpenFile(id)
	if err != nil {
		log.Printf("[fuse] OpenFile error for %s: %v", path, err)
		return -fuse.EIO
	}

	n, err := ra.ReadAt(buff, ofst)
	if err != nil && err != io.EOF {
		log.Printf("[fuse] ReadAt error for %s offset=%d: %v", path, ofst, err)
		if n > 0 {
			return n
		}
		return -fuse.EIO
	}

	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		log.Printf("[fuse] Read %s offset=%d len=%d → %d bytes in %v (cold)", path, ofst, len(buff), n, elapsed)
	}

	return n
}

func (fs *SOCILayerFS) Readlink(path string) (int, string) {
	_, attr, err := fs.lookupPath(path)
	if err != nil {
		return -fuse.ENOENT, ""
	}
	if attr.LinkName == "" {
		return -fuse.EINVAL, ""
	}
	return 0, attr.LinkName
}

// lookupPath resolves a FUSE path to a metadata node ID and attributes.
func (fs *SOCILayerFS) lookupPath(path string) (uint32, metadata.Attr, error) {
	p := toUnixPath(path)
	if p == "." || p == "" {
		attr, err := fs.meta.GetAttr(fs.meta.RootID())
		return fs.meta.RootID(), attr, err
	}

	parts := strings.Split(p, "/")
	id := fs.meta.RootID()
	var attr metadata.Attr
	var err error

	for _, part := range parts {
		if part == "" {
			continue
		}
		id, attr, err = fs.meta.GetChild(id, part)
		if err != nil {
			return 0, metadata.Attr{}, err
		}
	}

	// If we traversed parts, attr is already set from the last GetChild.
	// If not (empty path), get attr for root.
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == "") {
		attr, err = fs.meta.GetAttr(id)
		if err != nil {
			return 0, metadata.Attr{}, err
		}
	}

	return id, attr, nil
}

func toUnixPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "."
	}
	return p
}

func fillStatFromAttr(attr metadata.Attr, stat *fuse.Stat_t) {
	mode := attr.Mode
	if mode.IsDir() {
		stat.Mode = fuse.S_IFDIR | 0755
		stat.Size = 0
	} else if mode&os.ModeSymlink != 0 {
		stat.Mode = fuse.S_IFLNK | 0777
		stat.Size = int64(len(attr.LinkName))
	} else {
		stat.Mode = fuse.S_IFREG | 0644
		stat.Size = attr.Size
	}

	sec := attr.ModTime.Unix()
	nsec := int64(attr.ModTime.Nanosecond())
	stat.Atim = fuse.Timespec{Sec: sec, Nsec: nsec}
	stat.Mtim = fuse.Timespec{Sec: sec, Nsec: nsec}
	stat.Ctim = fuse.Timespec{Sec: sec, Nsec: nsec}
	stat.Nlink = 1
}
