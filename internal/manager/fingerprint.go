package manager

import (
	"errors"
	"fmt"
	"io"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/file"
	"github.com/stashapp/stash/pkg/hash/md5"
	"github.com/stashapp/stash/pkg/hash/oshash"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/strm"
)

type fingerprintCalculator struct {
	Config *config.Config
}

func (c *fingerprintCalculator) calculateOshash(f *models.BaseFile, o file.Opener) (*models.Fingerprint, error) {
	// For a .strm, hash the REMOTE video (first+last 64KB via HTTP Range), not the
	// tiny text file. This yields the same oshash as the real file, so the scene
	// matches stash-boxes on identify and dedups with the mounted copy. The text
	// file's own size is meaningless here, so fetch the real size via HEAD.
	if strm.IsStrmPath(f.Path) {
		url, err := strm.ReadURL(f.Path)
		if err != nil || url == "" {
			return nil, fmt.Errorf("reading .strm url for oshash: %w", err)
		}
		if strm.IsRemoteURL(url) {
			hash, err := oshash.FromHTTPAutoSize(url)
			if err != nil {
				return nil, fmt.Errorf("calculating remote oshash for .strm: %w", err)
			}
			return &models.Fingerprint{
				Type:        models.FingerprintTypeOshash,
				Fingerprint: hash,
			}, nil
		}
	}

	r, err := o.Open()
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}

	defer r.Close()

	rc, isRC := r.(io.ReadSeeker)
	if !isRC {
		return nil, errors.New("cannot calculate oshash for non-readcloser")
	}

	hash, err := oshash.FromReader(rc, f.Size)
	if err != nil {
		return nil, fmt.Errorf("calculating oshash: %w", err)
	}

	return &models.Fingerprint{
		Type:        models.FingerprintTypeOshash,
		Fingerprint: hash,
	}, nil
}

func (c *fingerprintCalculator) calculateMD5(o file.Opener) (*models.Fingerprint, error) {
	r, err := o.Open()
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}

	defer r.Close()

	hash, err := md5.FromReader(r)
	if err != nil {
		return nil, fmt.Errorf("calculating md5: %w", err)
	}

	return &models.Fingerprint{
		Type:        models.FingerprintTypeMD5,
		Fingerprint: hash,
	}, nil
}

func (c *fingerprintCalculator) CalculateFingerprints(f *models.BaseFile, o file.Opener, useExisting bool) ([]models.Fingerprint, error) {
	var ret []models.Fingerprint
	calculateMD5 := true

	// Lazy zip galleries: a picture inside a zip is registered from the zip's table
	// of contents alone. Its MD5 is filled in when it is first shown (see
	// internal/api/lazy_image.go), so the scan never reads its bytes.
	if f.ZipFileID != nil && !useAsVideo(f.Path) {
		return nil, nil
	}

	// A zip gets an oshash (64 KB from each end) instead of an MD5 over the whole
	// file. A zip scanned before this keeps its MD5 until it changes.
	if isZip(f.Path) {
		if useExisting {
			if fp := f.Fingerprints.For(models.FingerprintTypeOshash); fp != nil {
				return []models.Fingerprint{*fp}, nil
			}
			if f.Fingerprints.For(models.FingerprintTypeMD5) != nil {
				return f.Fingerprints, nil
			}
		}

		fp, err := c.calculateOshash(f, o)
		if err != nil {
			return nil, err
		}

		return []models.Fingerprint{*fp}, nil
	}

	if useAsVideo(f.Path) {
		var (
			fp  *models.Fingerprint
			err error
		)

		if useExisting {
			fp = f.Fingerprints.For(models.FingerprintTypeOshash)
		}

		if fp == nil {
			// calculate oshash first
			fp, err = c.calculateOshash(f, o)
			if err != nil {
				return nil, err
			}
		}

		ret = append(ret, *fp)

		// only calculate MD5 if enabled in config
		calculateMD5 = c.Config.IsCalculateMD5()
	}

	if calculateMD5 {
		var (
			fp  *models.Fingerprint
			err error
		)

		if useExisting {
			fp = f.Fingerprints.For(models.FingerprintTypeMD5)
		}

		if fp == nil {
			if useExisting {
				// log to indicate missing fingerprint is being calculated
				logger.Infof("Calculating checksum for %s ...", f.Path)
			}

			fp, err = c.calculateMD5(o)
			if err != nil {
				return nil, err
			}
		}

		ret = append(ret, *fp)
	}

	return ret, nil
}
