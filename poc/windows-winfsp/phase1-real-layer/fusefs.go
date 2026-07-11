package main

import (
	"log"
	"strings"

	"github.com/winfsp/cgofuse/fuse"
)

// LayerFS implements cgofuse filesystem interface backed by a FileTree + SpanFetcher.
type LayerFS struct {
	fuse.FileSystemBase
	tree    *FileTree
	fetcher *SpanFetcher
}

// NewLayerFS creates a new FUSE filesystem.
func NewLayerFS(tree *FileTree, fetcher *SpanFetcher) *LayerFS {
	return &LayerFS{
		tree:    tree,
		fetcher: fetcher,
	}
}

// toUnixPath converts Windows-style FUSE paths (backslash, leading /) to our tree paths.
func toUnixPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "."
	}
	return p
}

func (fs *LayerFS) Getattr(path string, stat *fuse.Stat_t, fh uint64) int {
	p := toUnixPath(path)
	node := fs.tree.Lookup(p)
	if node == nil {
		return -fuse.ENOENT
	}
	fillStat(node, stat)
	return 0
}

func (fs *LayerFS) Readdir(path string,
	fill func(name string, stat *fuse.Stat_t, ofst int64) bool,
	ofst int64, fh uint64) int {

	p := toUnixPath(path)
	node := fs.tree.Lookup(p)
	if node == nil {
		return -fuse.ENOENT
	}
	if !node.IsDir {
		return -fuse.ENOTDIR
	}

	// . and ..
	fill(".", nil, 0)
	fill("..", nil, 0)

	for _, child := range node.Children {
		var st fuse.Stat_t
		fillStat(child, &st)
		if !fill(child.Name, &st, 0) {
			break
		}
	}
	return 0
}

func (fs *LayerFS) Open(path string, flags int) (int, uint64) {
	p := toUnixPath(path)
	node := fs.tree.Lookup(p)
	if node == nil {
		return -fuse.ENOENT, 0
	}
	if node.IsDir {
		return -fuse.EISDIR, 0
	}
	// We don't use file handles; return 0.
	return 0, 0
}

func (fs *LayerFS) Read(path string, buff []byte, ofst int64, fh uint64) int {
	p := toUnixPath(path)
	node := fs.tree.Lookup(p)
	if node == nil {
		return -fuse.ENOENT
	}
	if node.IsDir {
		return -fuse.EISDIR
	}
	if node.UncompressedSize == 0 {
		return 0
	}

	log.Printf("[fuse] Read %s offset=%d len=%d", p, ofst, len(buff))

	n, err := fs.fetcher.ReadFile(
		node.UncompressedOffset,
		node.UncompressedSize,
		buff,
		ofst,
	)
	if err != nil {
		log.Printf("[fuse] Read error for %s: %v", p, err)
		// Return what we got (may be partial)
		if n > 0 {
			return n
		}
		return -fuse.EIO
	}
	return n
}

func (fs *LayerFS) Readlink(path string) (int, string) {
	p := toUnixPath(path)
	node := fs.tree.Lookup(p)
	if node == nil {
		return -fuse.ENOENT, ""
	}
	if node.LinkName == "" {
		return -fuse.EINVAL, ""
	}
	return 0, node.LinkName
}

func fillStat(node *FileNode, stat *fuse.Stat_t) {
	if node.IsDir {
		stat.Mode = fuse.S_IFDIR | 0755
		stat.Size = 0
	} else if node.LinkName != "" && node.Mode&0120000 != 0 {
		stat.Mode = fuse.S_IFLNK | 0777
		stat.Size = int64(len(node.LinkName))
	} else {
		stat.Mode = fuse.S_IFREG | 0644
		stat.Size = node.Size
	}
	// Set times
	sec := node.ModTime.Unix()
	nsec := int64(node.ModTime.Nanosecond())
	stat.Atim = fuse.Timespec{Sec: sec, Nsec: nsec}
	stat.Mtim = fuse.Timespec{Sec: sec, Nsec: nsec}
	stat.Ctim = fuse.Timespec{Sec: sec, Nsec: nsec}
	stat.Nlink = 1
}
