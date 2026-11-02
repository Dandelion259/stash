package image

import (
	"archive/zip"
	"context"
	"image"
	"image/png"
	iofs "io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/file"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noReadFS fails the test if anything is read through it.
type noReadFS struct {
	models.FS
	t *testing.T
}

func (f noReadFS) Open(name string) (iofs.ReadDirFile, error) {
	f.t.Errorf("picture %s was opened during the scan", name)
	return nil, os.ErrNotExist
}

func writeTestZip(t *testing.T) string {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "gallery.zip")
	out, err := os.Create(zipPath)
	require.NoError(t, err)
	zw := zip.NewWriter(out)
	w, err := zw.Create("1.png")
	require.NoError(t, err)
	require.NoError(t, png.Encode(w, image.NewRGBA(image.Rect(0, 0, 3, 2))))
	require.NoError(t, zw.Close())
	require.NoError(t, out.Close())
	return zipPath
}

func TestDecorateZipPictureIsLazy(t *testing.T) {
	d := &Decorator{}
	for path, format := range map[string]string{
		"/g.zip/1.jpg":  "jpeg",
		"/g.zip/2.JPEG": "jpeg",
		"/g.zip/3.png":  "png",
		"/g.zip/4.gif":  "gif",
		"/g.zip/5.webp": "webp",
		"/g.zip/6.jxl":  "jpegxl",
	} {
		f, err := d.Decorate(context.Background(), noReadFS{t: t}, &models.BaseFile{Path: path})
		require.NoError(t, err, path)
		imf, ok := f.(*models.ImageFile)
		require.True(t, ok, path)
		assert.Equal(t, format, imf.Format, path)
		assert.Zero(t, imf.Width, path)
		assert.Zero(t, imf.Height, path)
	}

	_, err := d.Decorate(context.Background(), noReadFS{t: t}, &models.BaseFile{Path: "/g.zip/7.avif"})
	assert.ErrorIs(t, err, ErrUnsupportedAVIFInZip)
}

func TestDecorateZipMemberReadsPicture(t *testing.T) {
	zipPath := writeTestZip(t)
	info, err := os.Stat(zipPath)
	require.NoError(t, err)
	zfs, err := (&file.OsFS{}).OpenZip(zipPath, info.Size())
	require.NoError(t, err)
	defer zfs.Close()

	d := &Decorator{}
	f, err := d.DecorateZipMember(zfs, &models.BaseFile{Path: filepath.Join(zipPath, "1.png")})
	require.NoError(t, err)
	imf := f.(*models.ImageFile)
	assert.Equal(t, "png", imf.Format)
	assert.Equal(t, 3, imf.Width)
	assert.Equal(t, 2, imf.Height)
}
