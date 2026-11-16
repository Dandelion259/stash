package manager

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/file"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/job"
)

// RegisterReleaseInput is a release folder as its source (nzbstream) knows it.
type RegisterReleaseInput struct {
	// Path is the release folder as stash sees it. It must be inside a library.
	Path string `json:"path"`
	// ModTime is the folder's modification time, in seconds since the epoch.
	ModTime int64 `json:"modTime"`
	// Entries are all files and folders below Path.
	Entries []*RegisterReleaseEntryInput `json:"entries"`
	// Identify queues, once the release is registered, the identification of its
	// scenes against every stash-box and the import of ThePornDB's markers.
	Identify *bool `json:"identify"`
	// StashIDs are the stash-box scenes the release was grabbed for. They are
	// used by Identify, for a release that is a single scene.
	StashIDs []*RegisterReleaseStashIDInput `json:"stashIds"`
}

type RegisterReleaseStashIDInput struct {
	Endpoint string `json:"endpoint"`
	StashID  string `json:"stashId"`
}

type RegisterReleaseEntryInput struct {
	// Path is relative to the release folder.
	Path  string `json:"path"`
	IsDir *bool  `json:"isDir"`
	Size  *int64 `json:"size"`
	// ModTime is in seconds since the epoch, as a scan of the folder would see it.
	ModTime int64 `json:"modTime"`
	// Oshash of the file. Without it stash reads the hash from the file itself.
	Oshash *string `json:"oshash"`
}

type RegisterReleaseResult struct {
	Scenes    []string `json:"scenes"`
	Galleries []string `json:"galleries"`
}

// RegisterRelease adds the files of a release to the library without walking its
// folder: names, sizes, modification times and hashes come from the input, so
// stash does not list the folder or read the hashes over a slow mount. Everything
// else is the scan: the same scanner runs over the listed tree, so it makes what
// a scan of the folder makes, and a later scan finds the files unchanged.
//
// It runs at once, next to whatever job is running, and returns when it is done.
func (s *Manager) RegisterRelease(ctx context.Context, input RegisterReleaseInput) (*RegisterReleaseResult, error) {
	if err := s.validateFFmpeg(); err != nil {
		return nil, err
	}

	root := filepath.Clean(input.Path)
	if !filepath.IsAbs(root) || config.GetInstance().GetStashPaths().GetStashFromDirPath(root) == nil {
		return nil, fmt.Errorf("%s is not in the configured stash paths", input.Path)
	}

	entries := make([]file.ListedEntry, 0, len(input.Entries))
	oshashes := make(map[string]string)
	for _, e := range input.Entries {
		if e == nil {
			continue
		}

		if !filepath.IsLocal(e.Path) {
			return nil, fmt.Errorf("%q is not a path inside the release", e.Path)
		}

		entry := file.ListedEntry{
			Path:    filepath.Join(root, e.Path),
			IsDir:   e.IsDir != nil && *e.IsDir,
			ModTime: time.Unix(e.ModTime, 0),
		}
		if e.Size != nil {
			entry.Size = *e.Size
		}
		entries = append(entries, entry)

		if e.Oshash != nil && *e.Oshash != "" {
			// the scanner looks the hash up by the path it stores
			oshashes[fsutil.NormalizePath(entry.Path)] = strings.ToLower(*e.Oshash)
		}
	}

	scanner := s.newScanner(false)
	scanner.FingerprintCalculator = &fingerprintCalculator{Config: s.Config, Oshashes: oshashes}

	scanJob := ScanJob{
		scanner:       scanner,
		input:         ScanMetadataInput{Paths: []string{root}},
		subscriptions: s.scanSubs,
		fs:            file.NewListedFS(file.ListedEntry{Path: root, ModTime: time.Unix(input.ModTime, 0)}, entries),
	}

	// the request may go away (a caller's timeout): what was started is finished
	err := s.JobManager.Run(context.WithoutCancel(ctx), "Registering "+filepath.Base(root)+"...", job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
		if err := scanJob.Execute(ctx, progress); err != nil {
			return err
		}
		if job.IsCancelled(ctx) {
			return errors.New("registering was stopped")
		}
		return nil
	}))
	if err != nil {
		return nil, err
	}

	// what the release is in the library now
	ret := &RegisterReleaseResult{Scenes: []string{}, Galleries: []string{}}
	r := s.Repository
	err = r.WithReadTxn(ctx, func(ctx context.Context) error {
		seenScene := make(map[int]bool)
		seenGallery := make(map[int]bool)

		for _, e := range entries {
			if e.IsDir {
				continue
			}

			path := fsutil.NormalizePath(e.Path)

			scenes, err := r.Scene.FindByPath(ctx, path)
			if err != nil {
				return err
			}
			for _, scene := range scenes {
				if !seenScene[scene.ID] {
					seenScene[scene.ID] = true
					ret.Scenes = append(ret.Scenes, strconv.Itoa(scene.ID))
				}
			}

			galleries, err := r.Gallery.FindByPath(ctx, path)
			if err != nil {
				return err
			}
			for _, gallery := range galleries {
				if !seenGallery[gallery.ID] {
					seenGallery[gallery.ID] = true
					ret.Galleries = append(ret.Galleries, strconv.Itoa(gallery.ID))
				}
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	if input.Identify != nil && *input.Identify && len(ret.Scenes) > 0 {
		known := make(map[string]string)
		for _, id := range input.StashIDs {
			if id != nil && id.Endpoint != "" && id.StashID != "" {
				known[id.Endpoint] = id.StashID
			}
		}

		s.JobManager.Add(context.WithoutCancel(ctx), "Identifying "+filepath.Base(root)+"...", &releaseIdentifyJob{
			release:       filepath.Base(root),
			sceneIDs:      scenesOf(ret.Scenes),
			knownStashIDs: known,
		})
	}

	return ret, nil
}
