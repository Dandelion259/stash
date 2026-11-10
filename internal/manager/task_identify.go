package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/stashapp/stash/internal/identify"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/match"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
	"github.com/stashapp/stash/pkg/stashbox"
	"github.com/stashapp/stash/pkg/txn"
)

var ErrInput = errors.New("invalid request input")

type IdentifyJob struct {
	postHookExecutor identify.SceneUpdatePostHookExecutor
	input            identify.Options

	stashBoxes []*models.StashBox
	progress   *job.Progress
}

func CreateIdentifyJob(input identify.Options) *IdentifyJob {
	return &IdentifyJob{
		postHookExecutor: instance.PluginCache,
		input:            input,
		stashBoxes:       instance.Config.GetStashBoxes(),
	}
}

func (j *IdentifyJob) Execute(ctx context.Context, progress *job.Progress) error {
	j.progress = progress

	// if no sources provided - just return
	if len(j.input.Sources) == 0 {
		return nil
	}

	sources, err := j.getSources()
	if err != nil {
		return err
	}

	// if scene ids provided, use those
	// otherwise, batch query for all scenes - ordering by path
	// don't use a transaction to query scenes
	r := instance.Repository
	if err := r.WithDB(ctx, func(ctx context.Context) error {
		if len(j.input.SceneIDs) == 0 {
			return j.identifyAllScenes(ctx, sources)
		}

		sceneIDs, err := stringslice.StringSliceToIntSlice(j.input.SceneIDs)
		if err != nil {
			return fmt.Errorf("invalid scene IDs: %w", err)
		}

		progress.SetTotal(len(sceneIDs))
		var batch []*models.Scene
		for _, id := range sceneIDs {
			if job.IsCancelled(ctx) {
				return nil
			}

			// find the scene
			var err error
			scene, err := r.Scene.Find(ctx, id)
			if err != nil {
				return fmt.Errorf("finding scene id %d: %w", id, err)
			}

			if scene == nil {
				return fmt.Errorf("scene with id %d not found", id)
			}

			batch = append(batch, scene)
			if len(batch) == identifyBatchSize {
				j.identifyBatch(ctx, batch, sources)
				batch = nil
			}
		}

		j.identifyBatch(ctx, batch, sources)
		return nil
	}); err != nil {
		return fmt.Errorf("error encountered while identifying scenes: %w", err)
	}

	return nil
}

func (j *IdentifyJob) identifyAllScenes(ctx context.Context, sources []identify.ScraperSource) error {
	r := instance.Repository

	// exclude organised
	organised := false
	sceneFilter := scene.FilterFromPaths(j.input.Paths)
	sceneFilter.Organized = &organised

	sort := "path"
	findFilter := &models.FindFilterType{
		Sort: &sort,
	}

	// get the count
	pp := 0
	findFilter.PerPage = &pp
	countResult, err := r.Scene.Query(ctx, models.SceneQueryOptions{
		QueryOptions: models.QueryOptions{
			FindFilter: findFilter,
			Count:      true,
		},
		SceneFilter: sceneFilter,
	})
	if err != nil {
		return fmt.Errorf("error getting scene count: %w", err)
	}

	j.progress.SetTotal(countResult.Count)

	var batch []*models.Scene
	if err := scene.BatchProcess(ctx, r.Scene, sceneFilter, findFilter, func(scene *models.Scene) error {
		if job.IsCancelled(ctx) {
			return nil
		}

		batch = append(batch, scene)
		if len(batch) == identifyBatchSize {
			j.identifyBatch(ctx, batch, sources)
			batch = nil
		}
		return nil
	}); err != nil {
		return err
	}

	j.identifyBatch(ctx, batch, sources)
	return nil
}

const (
	// identifyBatchSize is the number of scenes looked up with one stash-box
	// fingerprint query (the most stash-box accepts per query).
	identifyBatchSize = 40
	// identifyWorkers is the number of scenes identified at the same time.
	// Database writes stay serial; the scraping and the scene update plugin
	// hooks run in parallel.
	identifyWorkers = 4
)

// identifyBatch looks the scenes up on the stash-box sources with one query
// per source, then identifies them with identifyWorkers workers.
func (j *IdentifyJob) identifyBatch(ctx context.Context, scenes []*models.Scene, sources []identify.ScraperSource) {
	if len(scenes) == 0 || job.IsCancelled(ctx) {
		return
	}

	prefetchStashBoxes(ctx, scenes, sources)

	work := make(chan *models.Scene)
	var wg sync.WaitGroup
	for range identifyWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range work {
				j.identifyScene(ctx, s, sources)
			}
		}()
	}

	for _, s := range scenes {
		work <- s
	}
	close(work)
	wg.Wait()
}

// prefetchStashBoxes queries the stash-box sources in order, each for the
// scenes the sources before it did not match (identify uses the first source
// that matches). It stops at the first non stash-box source; the remaining
// sources are queried per scene as before.
func prefetchStashBoxes(ctx context.Context, scenes []*models.Scene, sources []identify.ScraperSource) {
	pending := scenes
	for _, src := range sources {
		sb, ok := src.Scraper.(stashboxSource)
		if !ok || len(pending) == 0 || job.IsCancelled(ctx) {
			return
		}
		pending = sb.prefetch(ctx, pending)
	}
}

func (j *IdentifyJob) identifyScene(ctx context.Context, s *models.Scene, sources []identify.ScraperSource) {
	if job.IsCancelled(ctx) {
		return
	}

	var taskError error
	j.progress.ExecuteTask("Identifying "+s.Path, func() {
		r := instance.Repository
		task := identify.SceneIdentifier{
			TxnManager:         r.TxnManager,
			SceneReaderUpdater: r.Scene,
			StudioReaderWriter: r.Studio,
			PerformerCreator:   r.Performer,
			TagFinderCreator:   r.Tag,

			DefaultOptions:              j.input.Options,
			Sources:                     sources,
			SceneUpdatePostHookExecutor: j.postHookExecutor,
			Matcher:                     newRelationshipMatcher(),
		}

		taskError = task.Identify(ctx, s)
	})

	if taskError != nil {
		logger.Errorf("Error encountered identifying %s: %v", s.Path, taskError)
	}

	j.progress.Increment()
}

func (j *IdentifyJob) getSources() ([]identify.ScraperSource, error) {
	var ret []identify.ScraperSource
	for _, source := range j.input.Sources {
		// get scraper source
		stashBox, err := j.getStashBox(source.Source)
		if err != nil {
			return nil, err
		}

		var src identify.ScraperSource
		if stashBox != nil {
			src = identify.ScraperSource{
				Name: "stash-box: " + stashBox.Endpoint,
				Scraper: stashboxSource{
					Client:                 stashbox.NewClient(*stashBox, stashbox.ExcludeTagPatterns(instance.Config.GetScraperExcludeTagPatterns())),
					endpoint:               stashBox.Endpoint,
					txnManager:             instance.Repository.TxnManager,
					sceneFingerprintGetter: instance.SceneService,
					matcher:                newRelationshipMatcher(),
					prefetched:             &prefetchedResults{m: map[int][]*models.ScrapedScene{}},
				},
				RemoteSite: stashBox.Endpoint,
			}
		} else {
			scraperID := *source.Source.ScraperID
			s := instance.ScraperCache.GetScraper(scraperID)
			if s == nil {
				return nil, fmt.Errorf("%w: scraper with id %q", models.ErrNotFound, scraperID)
			}
			src = identify.ScraperSource{
				Name: s.Name,
				Scraper: scraperSource{
					cache:     instance.ScraperCache,
					scraperID: scraperID,
				},
			}
		}

		src.Options = source.Options
		ret = append(ret, src)
	}

	return ret, nil
}

func (j *IdentifyJob) getStashBox(src *scraper.Source) (*models.StashBox, error) {
	if src.ScraperID != nil {
		return nil, nil
	}

	// must be stash-box
	if src.StashBoxIndex == nil && src.StashBoxEndpoint == nil {
		return nil, fmt.Errorf("%w: stash_box_index or stash_box_endpoint or scraper_id must be set", ErrInput)
	}

	return resolveStashBox(j.stashBoxes, *src)
}

func resolveStashBox(sb []*models.StashBox, source scraper.Source) (*models.StashBox, error) {
	if source.StashBoxIndex != nil {
		index := source.StashBoxIndex
		if *index < 0 || *index >= len(sb) {
			return nil, fmt.Errorf("%w: invalid stash_box_index: %d", models.ErrScraperSource, index)
		}

		return sb[*index], nil
	}

	if source.StashBoxEndpoint != nil {
		var ret *models.StashBox
		endpoint := *source.StashBoxEndpoint
		for _, b := range sb {
			if strings.EqualFold(endpoint, b.Endpoint) {
				ret = b
			}
		}

		if ret == nil {
			return nil, fmt.Errorf(`%w: stash-box with endpoint "%s"`, models.ErrNotFound, endpoint)
		}

		return ret, nil
	}

	// neither stash-box inputs were provided, so assume it is a scraper

	return nil, nil
}

type stashboxSource struct {
	*stashbox.Client
	endpoint string

	txnManager             models.TxnManager
	sceneFingerprintGetter sceneFingerprintGetter
	matcher                match.SceneRelationships

	// results of the batched fingerprint query, by scene ID
	prefetched *prefetchedResults
}

func newRelationshipMatcher() match.SceneRelationships {
	return match.SceneRelationships{
		PerformerFinder: instance.Repository.Performer,
		TagFinder:       instance.Repository.Tag,
		StudioFinder:    instance.Repository.Studio,
	}
}

type prefetchedResults struct {
	mu sync.Mutex
	m  map[int][]*models.ScrapedScene
}

// take returns and forgets the prefetched results of a scene. ok is false if
// the scene was not prefetched.
func (p *prefetchedResults) take(sceneID int) (results []*models.ScrapedScene, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	results, ok = p.m[sceneID]
	delete(p.m, sceneID)
	return results, ok
}

type sceneFingerprintGetter interface {
	GetScenesFingerprints(ctx context.Context, ids []int) ([]models.Fingerprints, error)
}

// prefetch looks the scenes up with one fingerprint query and keeps the
// results for ScrapeScenes. It returns the scenes without a match. On error
// nothing is kept and ScrapeScenes queries per scene.
func (s stashboxSource) prefetch(ctx context.Context, scenes []*models.Scene) []*models.Scene {
	ids := make([]int, len(scenes))
	for i, sc := range scenes {
		ids[i] = sc.ID
	}

	var fps []models.Fingerprints
	if err := txn.WithReadTxn(ctx, s.txnManager, func(ctx context.Context) error {
		var err error
		fps, err = s.sceneFingerprintGetter.GetScenesFingerprints(ctx, ids)
		return err
	}); err != nil {
		logger.Warnf("Identify: getting fingerprints for batch lookup on %s: %v", s.endpoint, err)
		return scenes
	}

	results, err := s.FindScenesByFingerprints(ctx, fps)
	if err != nil || len(results) != len(scenes) {
		logger.Warnf("Identify: batch lookup on %s failed, querying per scene: %v", s.endpoint, err)
		return scenes
	}

	var unmatched []*models.Scene
	s.prefetched.mu.Lock()
	defer s.prefetched.mu.Unlock()
	for i, sc := range scenes {
		s.prefetched.m[sc.ID] = results[i]
		if len(results[i]) == 0 {
			unmatched = append(unmatched, sc)
		}
	}
	return unmatched
}

func (s stashboxSource) ScrapeScenes(ctx context.Context, sceneID int) ([]*models.ScrapedScene, error) {
	results, ok := s.prefetched.take(sceneID)
	if !ok {
		var fps []models.Fingerprints
		if err := txn.WithReadTxn(ctx, s.txnManager, func(ctx context.Context) error {
			var err error
			fps, err = s.sceneFingerprintGetter.GetScenesFingerprints(ctx, []int{sceneID})
			return err
		}); err != nil {
			return nil, fmt.Errorf("error getting scene fingerprints: %w", err)
		}

		var err error
		results, err = s.FindSceneByFingerprints(ctx, fps[0])
		if err != nil {
			return nil, fmt.Errorf("error querying stash-box using scene ID %d: %w", sceneID, err)
		}
	}

	if err := txn.WithReadTxn(ctx, s.txnManager, func(ctx context.Context) error {
		for _, ret := range results {
			if err := s.matcher.MatchRelationships(ctx, ret, s.endpoint); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("error matching scene relationships: %w", err)
	}

	if len(results) > 0 {
		return results, nil
	}

	return nil, nil
}

func (s stashboxSource) String() string {
	return fmt.Sprintf("stash-box %s", s.endpoint)
}

type scraperSource struct {
	cache     *scraper.Cache
	scraperID string
}

func (s scraperSource) ScrapeScenes(ctx context.Context, sceneID int) ([]*models.ScrapedScene, error) {
	content, err := s.cache.ScrapeID(ctx, s.scraperID, sceneID, scraper.ScrapeContentTypeScene)
	if err != nil {
		return nil, err
	}

	// don't try to convert nil return value
	if content == nil {
		return nil, nil
	}

	if scene, ok := content.(models.ScrapedScene); ok {
		return []*models.ScrapedScene{&scene}, nil
	}

	return nil, errors.New("could not convert content to scene")
}

func (s scraperSource) String() string {
	return fmt.Sprintf("scraper %s", s.scraperID)
}
