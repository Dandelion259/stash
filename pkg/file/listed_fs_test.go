package file

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListedFS(t *testing.T) {
	// one real file, so that file contents are shown to come from the disk
	dir := t.TempDir()
	root := filepath.Join(dir, "release")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0755))
	video := filepath.Join(root, "sub", "b.mp4")
	require.NoError(t, os.WriteFile(video, []byte("on disk"), 0644))

	modTime := time.Unix(1790864418, 0)
	listed := NewListedFS(
		ListedEntry{Path: root, ModTime: modTime},
		[]ListedEntry{
			// sub is not listed: it is made up
			{Path: video, Size: 7056297794, ModTime: modTime},
			{Path: filepath.Join(root, "a.zip"), Size: 5, ModTime: modTime},
			{Path: filepath.Join(dir, "elsewhere.mp4"), Size: 1},
		},
	)

	assert.True(t, IsOnDisk(listed))
	assert.True(t, IsOnDisk(&OsFS{}))

	// sizes and times are the listed ones, not the disk's
	info, err := listed.Lstat(video)
	require.NoError(t, err)
	assert.Equal(t, int64(7056297794), info.Size())
	assert.Equal(t, modTime, info.ModTime())
	assert.Equal(t, "b.mp4", info.Name())
	assert.False(t, info.IsDir())

	// on disk but not listed, and listed but outside the root
	require.NoError(t, os.WriteFile(filepath.Join(root, "unlisted.mp4"), nil, 0644))
	_, err = listed.Lstat(filepath.Join(root, "unlisted.mp4"))
	assert.True(t, errors.Is(err, fs.ErrNotExist))
	_, err = listed.Lstat(filepath.Join(dir, "elsewhere.mp4"))
	assert.True(t, errors.Is(err, fs.ErrNotExist))

	// the walk sees the listed tree, in name order
	var walked []string
	require.NoError(t, SymWalk(listed, root, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		rel, _ := filepath.Rel(root, path)
		walked = append(walked, filepath.ToSlash(rel))
		return nil
	}))
	assert.Equal(t, []string{".", "a.zip", "sub", "sub/b.mp4"}, walked)

	// contents come from the disk
	f, err := listed.Open(video)
	require.NoError(t, err)
	defer f.Close()
	buf := make([]byte, 16)
	n, _ := f.Read(buf)
	assert.Equal(t, "on disk", string(buf[:n]))
}
