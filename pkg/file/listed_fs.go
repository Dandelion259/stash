package file

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

// ListedEntry is a file or folder of a ListedFS.
type ListedEntry struct {
	Path    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// ListedFS is the OS file system with its directory tree taken from a list
// instead of the disk: Lstat, Stat and reading a directory answer from the list,
// the contents of a file still come from the disk.
//
// It lets a release be registered from what its source already knows (names,
// sizes, modification times) without walking a slow mount for it.
type ListedFS struct {
	OsFS
	entries  map[string]ListedEntry
	children map[string][]string
}

// NewListedFS makes a file system of root and the entries below it. A folder
// between root and an entry that is not listed itself is made up, with the zero
// modification time. Entries outside root are left out.
func NewListedFS(root ListedEntry, entries []ListedEntry) *ListedFS {
	ret := &ListedFS{
		entries:  make(map[string]ListedEntry),
		children: make(map[string][]string),
	}

	root.Path = filepath.Clean(root.Path)
	root.IsDir = true
	ret.entries[root.Path] = root

	for _, e := range entries {
		e.Path = filepath.Clean(e.Path)
		if rel, err := filepath.Rel(root.Path, e.Path); err != nil || rel == "." || rel == ".." || len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator) {
			continue
		}
		ret.entries[e.Path] = e
	}

	linked := make(map[string]bool)
	for path := range ret.entries {
		for path != root.Path && !linked[path] {
			linked[path] = true
			parent := filepath.Dir(path)
			ret.children[parent] = append(ret.children[parent], path)
			path = parent
		}
	}

	return ret
}

// IsOnDisk returns true if the contents of the files of fs can be read from the
// same path on the OS file system.
func IsOnDisk(fs models.FS) bool {
	switch fs.(type) {
	case *OsFS, *ListedFS:
		return true
	}

	return false
}

func (f *ListedFS) info(name string) (listedInfo, error) {
	name = filepath.Clean(name)

	if e, ok := f.entries[name]; ok {
		return listedInfo{e}, nil
	}

	if _, ok := f.children[name]; ok {
		return listedInfo{ListedEntry{Path: name, IsDir: true}}, nil
	}

	return listedInfo{}, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrNotExist}
}

func (f *ListedFS) Stat(name string) (fs.FileInfo, error) {
	return f.info(name)
}

func (f *ListedFS) Lstat(name string) (fs.FileInfo, error) {
	return f.info(name)
}

// Open opens a listed folder from the list and anything else from the disk.
func (f *ListedFS) Open(name string) (fs.ReadDirFile, error) {
	info, err := f.info(name)
	if err != nil || !info.IsDir() {
		return os.Open(name)
	}

	children := f.children[filepath.Clean(name)]
	entries := make([]fs.DirEntry, 0, len(children))
	for _, c := range children {
		childInfo, err := f.info(c)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fs.FileInfoToDirEntry(childInfo))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	return &listedDir{info: info, entries: entries}, nil
}

type listedInfo struct {
	ListedEntry
}

func (i listedInfo) Name() string       { return filepath.Base(i.Path) }
func (i listedInfo) Size() int64        { return i.ListedEntry.Size }
func (i listedInfo) ModTime() time.Time { return i.ListedEntry.ModTime }
func (i listedInfo) IsDir() bool        { return i.ListedEntry.IsDir }
func (i listedInfo) Sys() any           { return nil }

func (i listedInfo) Mode() fs.FileMode {
	if i.ListedEntry.IsDir {
		return fs.ModeDir | 0555
	}

	return 0444
}

type listedDir struct {
	info    listedInfo
	entries []fs.DirEntry
}

func (d *listedDir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *listedDir) Close() error               { return nil }

func (d *listedDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.Path, Err: errors.New("is a directory")}
}

func (d *listedDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		ret := d.entries
		d.entries = nil
		return ret, nil
	}

	if len(d.entries) == 0 {
		return nil, io.EOF
	}

	if n > len(d.entries) {
		n = len(d.entries)
	}

	ret := d.entries[:n]
	d.entries = d.entries[n:]
	return ret, nil
}
