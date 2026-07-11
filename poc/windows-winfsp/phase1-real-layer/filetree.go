package main

import (
	"os"
	"path"
	"strings"
	"time"

	"github.com/awslabs/soci-snapshotter/ztoc"
)

// FileNode represents a file or directory in the layer.
type FileNode struct {
	Name               string
	IsDir              bool
	Size               int64
	Mode               os.FileMode
	ModTime            time.Time
	LinkName           string
	UncompressedOffset int64
	UncompressedSize   int64
	Children           map[string]*FileNode // nil for files
}

// FileTree is the root of the parsed file system.
type FileTree struct {
	Root *FileNode
}

// BuildFileTree constructs a FileTree from zTOC file metadata.
func BuildFileTree(z *ztoc.Ztoc) *FileTree {
	root := &FileNode{
		Name:    "",
		IsDir:   true,
		Mode:    os.ModeDir | 0755,
		ModTime: time.Now(),
		Children: make(map[string]*FileNode),
	}

	for _, entry := range z.TOC.FileMetadata {
		name := cleanPath(entry.Name)
		if name == "" || name == "." {
			continue
		}

		isDir := entry.Type == "dir"
		node := &FileNode{
			Name:               path.Base(name),
			IsDir:              isDir,
			Size:               int64(entry.UncompressedSize),
			Mode:               entry.FileMode(),
			ModTime:            entry.ModTime,
			LinkName:           entry.Linkname,
			UncompressedOffset: int64(entry.UncompressedOffset),
			UncompressedSize:   int64(entry.UncompressedSize),
		}
		if isDir {
			node.Children = make(map[string]*FileNode)
		}

		// Ensure all parent dirs exist, then insert
		parent := ensureParents(root, path.Dir(name))
		parent.Children[node.Name] = node
	}

	return &FileTree{Root: root}
}

// Lookup finds a node by path (using forward slashes, no leading slash).
func (ft *FileTree) Lookup(p string) *FileNode {
	p = cleanPath(p)
	if p == "" || p == "." || p == "/" {
		return ft.Root
	}

	parts := strings.Split(p, "/")
	cur := ft.Root
	for _, part := range parts {
		if part == "" {
			continue
		}
		if cur.Children == nil {
			return nil
		}
		child, ok := cur.Children[part]
		if !ok {
			return nil
		}
		cur = child
	}
	return cur
}

// ReadDir returns children of a directory node.
func (ft *FileTree) ReadDir(p string) []*FileNode {
	node := ft.Lookup(p)
	if node == nil || !node.IsDir {
		return nil
	}
	result := make([]*FileNode, 0, len(node.Children))
	for _, child := range node.Children {
		result = append(result, child)
	}
	return result
}

func ensureParents(root *FileNode, dirPath string) *FileNode {
	dirPath = cleanPath(dirPath)
	if dirPath == "" || dirPath == "." || dirPath == "/" {
		return root
	}
	parts := strings.Split(dirPath, "/")
	cur := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		child, ok := cur.Children[part]
		if !ok {
			child = &FileNode{
				Name:     part,
				IsDir:    true,
				Mode:     os.ModeDir | 0755,
				ModTime:  time.Now(),
				Children: make(map[string]*FileNode),
			}
			cur.Children[part] = child
		}
		cur = child
	}
	return cur
}

func cleanPath(p string) string {
	// Normalize: remove leading ./ or / , convert backslash
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	return p
}
