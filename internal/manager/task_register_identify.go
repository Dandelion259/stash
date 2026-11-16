package manager

import (
	"context"
	"math"
	"strconv"

	"github.com/stashapp/stash/internal/identify"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scraper"
)

// releaseIdentifyJob identifies the scenes of a registered release against
// every configured stash-box, one after the other, with the saved Identify
// options, then adds ThePornDB's markers.
//
// One pass per stash-box and not one Identify with all of them as sources: the
// sources of one Identify are a fallback chain, only the first that finds a
// match is used, which would leave every scene with a single stash id.
//
// ThePornDB comes last, whatever its place in the settings: where two
// stash-boxes know a scene, the values of the last pass are the ones kept, and
// that was ThePornDB with the watcher this job replaces.
type releaseIdentifyJob struct {
	release  string
	sceneIDs []int

	// knownStashIDs is the scene the release was grabbed for, by stash-box
	// endpoint. It is only used for a release of a single scene.
	knownStashIDs map[string]string
}

// knownScene is a scene whose stash id on a stash-box is known beforehand.
type knownScene struct {
	stashID string
	// duration of the scene's file in seconds, 0 if unknown
	duration float64
}

// knownSceneTolerance is how far the duration of a file may be from the
// duration the stash-box has for the scene it was grabbed for: 15 s, or 1% of a
// long scene. A file further off is matched by its fingerprints like any other.
func knownSceneTolerance(duration float64) float64 {
	return math.Max(15, duration/100)
}

func (j *releaseIdentifyJob) Execute(ctx context.Context, progress *job.Progress) error {
	cfg := instance.Config

	var options *identify.MetadataOptions
	if defaults := cfg.GetDefaultIdentifySettings(); defaults != nil {
		options = defaults.Options
	}

	ids := make([]string, len(j.sceneIDs))
	for i, id := range j.sceneIDs {
		ids[i] = strconv.Itoa(id)
	}

	// the file's duration, to check a known scene against
	var duration float64
	if len(j.sceneIDs) == 1 && len(j.knownStashIDs) > 0 {
		r := instance.Repository
		if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
			scene, err := r.Scene.Find(ctx, j.sceneIDs[0])
			if err != nil || scene == nil {
				return err
			}
			if err := scene.LoadPrimaryFile(ctx, r.File); err != nil {
				return err
			}
			if f := scene.Files.Primary(); f != nil {
				duration = f.Duration
			}
			return nil
		}); err != nil {
			logger.Warnf("Identifying %s: reading the file's duration: %v", j.release, err)
		}
	}

	for _, box := range identifyOrder(cfg.GetStashBoxes()) {
		if job.IsCancelled(ctx) {
			return nil
		}

		// Every pass has the saved options, "organized" included: a scene any
		// stash-box knows is organized. (The watcher could only set it in its last
		// pass, as an Identify by folder leaves organized scenes out.)
		endpoint := box.Endpoint
		pass := CreateIdentifyJob(identify.Options{
			Sources:  []*identify.Source{{Source: &scraper.Source{StashBoxEndpoint: &endpoint}}},
			Options:  options,
			SceneIDs: ids,
		})
		if stashID := j.knownStashIDs[endpoint]; stashID != "" && len(j.sceneIDs) == 1 {
			pass.knownScenes = map[int]knownScene{j.sceneIDs[0]: {stashID: stashID, duration: duration}}
		}

		if err := pass.Execute(ctx, progress); err != nil {
			logger.Errorf("Identifying %s against %s: %v", j.release, endpoint, err)
			continue
		}

		if isTPDB(endpoint) {
			if created := importTPDBMarkers(ctx, box, j.sceneIDs); created > 0 {
				logger.Infof("Added %d ThePornDB markers to %s", created, j.release)
			}
		}
	}

	return nil
}

// identifyOrder is the order the stash-boxes are asked in: as configured, but
// ThePornDB last.
func identifyOrder(boxes []*models.StashBox) []*models.StashBox {
	ret := make([]*models.StashBox, 0, len(boxes))
	var last []*models.StashBox
	for _, box := range boxes {
		if isTPDB(box.Endpoint) {
			last = append(last, box)
		} else {
			ret = append(ret, box)
		}
	}
	return append(ret, last...)
}

// scenesOf returns the ids as ints, leaving out what is not a number.
func scenesOf(ids []string) []int {
	ret := make([]int, 0, len(ids))
	for _, id := range ids {
		if n, err := strconv.Atoi(id); err == nil {
			ret = append(ret, n)
		}
	}
	return ret
}

// knownMatch picks, from what a stash-box answered for a stash id, the scene
// with that id, unless its durations say the file is something else: the
// stash-box has a duration for the scene itself and one for each file
// (fingerprint) submitted for it, and none of them is near the file's.
func knownMatch(results []*models.ScrapedScene, known knownScene) *models.ScrapedScene {
	for _, r := range results {
		if r == nil || r.RemoteSiteID == nil || *r.RemoteSiteID != known.stashID {
			continue
		}

		var durations []float64
		if r.Duration != nil && *r.Duration > 0 {
			durations = append(durations, float64(*r.Duration))
		}
		for _, fp := range r.Fingerprints {
			if fp != nil && fp.Duration > 0 {
				durations = append(durations, float64(fp.Duration))
			}
		}

		// nothing to compare: the id is taken
		if known.duration <= 0 || len(durations) == 0 {
			return r
		}

		for _, d := range durations {
			if math.Abs(d-known.duration) <= knownSceneTolerance(known.duration) {
				return r
			}
		}

		return nil
	}

	return nil
}
