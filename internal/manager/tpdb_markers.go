package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/tag"
)

// ThePornDB keeps markers for its scenes, which its stash-box API does not hand
// out. After a release was identified against ThePornDB they are fetched from
// its REST API and added to the scenes that have none yet.
//
// This is what the nzbstream watcher did after its ThePornDB Identify pass (and
// the community TPDB Markers plugin before it, from a hook on every scene
// update), with the same rules as stashapi's import_scene_markers: markers
// within 15 s of each other become one, the earliest, which takes the names of
// the others as extra tags; a marker at 0 s is left out; the marker's name is
// its primary tag, made if no tag has that name or alias.
const (
	tpdbEndpoint      = "https://theporndb.net/graphql"
	tpdbSceneURL      = "https://api.theporndb.net/scenes/%s"
	tpdbMarkerMerge   = 15.0
	tpdbFetchAttempts = 6
)

func isTPDB(endpoint string) bool {
	return strings.TrimSuffix(endpoint, "/") == tpdbEndpoint
}

type tpdbMarker struct {
	Title   string
	Seconds float64
	// Tags are the names of the markers merged into this one.
	Tags []string
}

// mergeTPDBMarkers puts markers within 15 s of each other together, as
// stashapi's merge_markers does: sorted by time, a marker joins the first group
// that has a member closer than 15 s, and each group becomes its first marker.
// Markers at 0 s are left out.
func mergeTPDBMarkers(markers []tpdbMarker) []tpdbMarker {
	sort.SliceStable(markers, func(i, j int) bool { return markers[i].Seconds < markers[j].Seconds })

	var groups [][]tpdbMarker
	for _, m := range markers {
		joined := false
		for g := range groups {
			for _, member := range groups[g] {
				if math.Abs(m.Seconds-member.Seconds) < tpdbMarkerMerge {
					groups[g] = append(groups[g], m)
					joined = true
					break
				}
			}
			if joined {
				break
			}
		}
		if !joined {
			groups = append(groups, []tpdbMarker{m})
		}
	}

	var ret []tpdbMarker
	for _, group := range groups {
		first := group[0]
		if first.Seconds == 0 {
			continue
		}

		seen := map[string]bool{strings.ToLower(first.Title): true}
		for _, other := range group[1:] {
			if key := strings.ToLower(other.Title); !seen[key] {
				seen[key] = true
				first.Tags = append(first.Tags, other.Title)
			}
		}
		ret = append(ret, first)
	}

	return ret
}

// fetchTPDBMarkers gets the markers ThePornDB has for one of its scenes.
func fetchTPDBMarkers(ctx context.Context, apiKey string, tpdbID string) ([]tpdbMarker, error) {
	client := &http.Client{Timeout: time.Minute}

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(tpdbSceneURL, tpdbID), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")

		resp, err = client.Do(req)
		if err != nil {
			return nil, err
		}

		// 429 is ThePornDB's rate limit: wait and try again
		if resp.StatusCode != http.StatusTooManyRequests || attempt == tpdbFetchAttempts-1 {
			break
		}
		resp.Body.Close()

		wait := math.Pow(2, float64(attempt))
		if after, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil {
			wait = after
		}
		select {
		case <-time.After(time.Duration(math.Min(math.Max(wait, 1), 60) * float64(time.Second))):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad response from ThePornDB: %d", resp.StatusCode)
	}

	var body struct {
		Data struct {
			Markers []struct {
				Title     string  `json:"title"`
				StartTime float64 `json:"start_time"`
			} `json:"markers"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("reading ThePornDB's answer: %w", err)
	}

	ret := make([]tpdbMarker, 0, len(body.Data.Markers))
	for _, m := range body.Data.Markers {
		if title := strings.TrimSpace(m.Title); title != "" {
			ret = append(ret, tpdbMarker{Title: title, Seconds: m.StartTime})
		}
	}
	return ret, nil
}

// importTPDBMarkers adds ThePornDB's markers to those of the scenes that have a
// ThePornDB id and no markers. Returns the number of markers made.
func importTPDBMarkers(ctx context.Context, box *models.StashBox, sceneIDs []int) int {
	r := instance.Repository
	created := 0

	for _, sceneID := range sceneIDs {
		if ctx.Err() != nil {
			return created
		}

		// the scene's ThePornDB id, if it has no markers yet
		tpdbID := ""
		if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
			scene, err := r.Scene.Find(ctx, sceneID)
			if err != nil || scene == nil {
				return err
			}

			existing, err := r.SceneMarker.FindBySceneID(ctx, sceneID)
			if err != nil || len(existing) > 0 {
				return err
			}

			if err := scene.LoadStashIDs(ctx, r.Scene); err != nil {
				return err
			}
			for _, id := range scene.StashIDs.List() {
				if id.Endpoint == box.Endpoint {
					tpdbID = id.StashID
				}
			}
			return nil
		}); err != nil {
			logger.Errorf("ThePornDB markers: scene %d: %v", sceneID, err)
			continue
		}
		if tpdbID == "" {
			continue
		}

		fetched, err := fetchTPDBMarkers(ctx, box.APIKey, tpdbID)
		if err != nil {
			logger.Errorf("ThePornDB markers: scene %d (%s): %v", sceneID, tpdbID, err)
			continue
		}

		markers := mergeTPDBMarkers(fetched)
		if len(markers) == 0 {
			continue
		}

		if err := r.WithTxn(ctx, func(ctx context.Context) error {
			for _, m := range markers {
				primary, err := findOrCreateTag(ctx, r.Tag, m.Title)
				if err != nil {
					return err
				}

				var tagIDs []int
				for _, name := range m.Tags {
					t, err := findOrCreateTag(ctx, r.Tag, name)
					if err != nil {
						return err
					}
					if t.ID != primary.ID {
						tagIDs = append(tagIDs, t.ID)
					}
				}

				marker := models.NewSceneMarker()
				marker.Title = m.Title
				marker.Seconds = m.Seconds
				marker.PrimaryTagID = primary.ID
				marker.SceneID = sceneID
				if err := r.SceneMarker.Create(ctx, &marker); err != nil {
					return err
				}
				if err := r.SceneMarker.UpdateTags(ctx, marker.ID, tagIDs); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			logger.Errorf("ThePornDB markers: scene %d: %v", sceneID, err)
			continue
		}

		created += len(markers)
	}

	return created
}

// findOrCreateTag finds the tag with this name or alias, or makes it.
func findOrCreateTag(ctx context.Context, qb models.TagReaderWriter, name string) (*models.Tag, error) {
	t, err := tag.ByName(ctx, qb, name)
	if err != nil {
		return nil, err
	}
	if t == nil {
		if t, err = tag.ByAlias(ctx, qb, name); err != nil {
			return nil, err
		}
	}
	if t != nil {
		return t, nil
	}

	newTag := models.NewTag()
	newTag.Name = name
	if err := qb.Create(ctx, &models.CreateTagInput{Tag: &newTag}); err != nil {
		return nil, err
	}
	return &newTag, nil
}
