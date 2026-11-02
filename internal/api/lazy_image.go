package api

import (
	"context"
	"fmt"
	"sync"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/file"
	fileimage "github.com/stashapp/stash/pkg/file/image"
	"github.com/stashapp/stash/pkg/hash/md5"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

// Lazy zip galleries: the scan registers pictures inside zips without reading them
// (no MD5, width and height 0). The first time such a picture is served, it is read
// once more in the background and its MD5, format, width and height are stored.

var (
	lazyFillSlots    = make(chan struct{}, 2)
	lazyFillInFlight sync.Map
)

func isLazyImageFile(f *models.ImageFile) bool {
	return f.ZipFile != nil && f.Fingerprints.For(models.FingerprintTypeMD5) == nil
}

// fillLazyImage starts the fill for a lazy picture. Call it after the response is
// served: the fill changes f.
func fillLazyImage(f models.File) {
	imf, ok := f.(*models.ImageFile)
	if !ok || !isLazyImageFile(imf) {
		return
	}

	if _, busy := lazyFillInFlight.LoadOrStore(imf.ID, struct{}{}); busy {
		return
	}

	go func() {
		defer lazyFillInFlight.Delete(imf.ID)

		lazyFillSlots <- struct{}{}
		defer func() { <-lazyFillSlots }()

		if err := fillZipImage(imf); err != nil {
			logger.Warnf("filling in picture details for %s: %v", imf.Path, err)
		}
	}()
}

func fillZipImage(f *models.ImageFile) error {
	osFS := &file.OsFS{}

	r, err := f.Open(osFS)
	if err != nil {
		return err
	}
	hash, err := md5.FromReader(r)
	r.Close()
	if err != nil {
		return fmt.Errorf("calculating md5: %w", err)
	}

	zipFile := f.ZipFile.Base()
	zfs, err := osFS.OpenZip(zipFile.Path, zipFile.Size)
	if err != nil {
		return err
	}
	defer zfs.Close()

	mgr := manager.GetInstance()
	decorator := fileimage.Decorator{FFProbe: mgr.FFProbe}
	filled, err := decorator.DecorateZipMember(zfs, f)
	if err != nil {
		return err
	}

	filled.Base().SetFingerprint(models.Fingerprint{
		Type:        models.FingerprintTypeMD5,
		Fingerprint: hash,
	})

	return mgr.Repository.WithTxn(context.Background(), func(ctx context.Context) error {
		return mgr.Repository.File.Update(ctx, filled)
	})
}
