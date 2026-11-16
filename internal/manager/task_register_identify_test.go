package manager

import (
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
)

func TestKnownMatch(t *testing.T) {
	id, other := "f222413b-e780-45a1-b593-7a00cfeae6fb", "00000000-0000-0000-0000-000000000000"
	duration := func(d int) *int { return &d }
	results := []*models.ScrapedScene{
		{RemoteSiteID: &other, Duration: duration(1800)},
		{RemoteSiteID: &id, Duration: duration(1800)},
	}

	// the scene with the id, when the file is about as long
	assert.Same(t, results[1], knownMatch(results, knownScene{stashID: id, duration: 1806}))
	assert.Same(t, results[1], knownMatch(results, knownScene{stashID: id, duration: 1785}))
	// 1% of a long file is more than 15 s
	long := []*models.ScrapedScene{{RemoteSiteID: &id, Duration: duration(7200)}}
	assert.NotNil(t, knownMatch(long, knownScene{stashID: id, duration: 7260}))

	// another duration: this file is not that scene
	assert.Nil(t, knownMatch(results, knownScene{stashID: id, duration: 1830}))
	assert.Nil(t, knownMatch(results, knownScene{stashID: id, duration: 600}))

	// nothing to compare: the id is taken
	assert.Same(t, results[1], knownMatch(results, knownScene{stashID: id}))
	noDuration := []*models.ScrapedScene{{RemoteSiteID: &id}}
	assert.Same(t, noDuration[0], knownMatch(noDuration, knownScene{stashID: id, duration: 1800}))

	// no duration for the scene itself: the durations of its files count
	fps := func(d ...int) []*models.StashBoxFingerprint {
		var ret []*models.StashBoxFingerprint
		for _, v := range d {
			ret = append(ret, &models.StashBoxFingerprint{Algorithm: "OSHASH", Duration: v})
		}
		return ret
	}
	byFiles := []*models.ScrapedScene{{RemoteSiteID: &id, Fingerprints: fps(2454, 2451, 0)}}
	assert.Nil(t, knownMatch(byFiles, knownScene{stashID: id, duration: 2296}))
	assert.Same(t, byFiles[0], knownMatch(byFiles, knownScene{stashID: id, duration: 2449}))
	// one file of the right length is enough
	mixed := []*models.ScrapedScene{{RemoteSiteID: &id, Duration: duration(600), Fingerprints: fps(2454, 2297)}}
	assert.Same(t, mixed[0], knownMatch(mixed, knownScene{stashID: id, duration: 2296}))

	// the stash-box answered other scenes, or nothing
	assert.Nil(t, knownMatch(results[:1], knownScene{stashID: id, duration: 1800}))
	assert.Nil(t, knownMatch(nil, knownScene{stashID: id}))
}

func TestIdentifyOrder(t *testing.T) {
	tpdb := &models.StashBox{Endpoint: "https://theporndb.net/graphql"}
	stashdb := &models.StashBox{Endpoint: "https://stashdb.org/graphql"}
	other := &models.StashBox{Endpoint: "https://fansdb.cc/graphql"}

	assert.Equal(t, []*models.StashBox{stashdb, other, tpdb}, identifyOrder([]*models.StashBox{tpdb, stashdb, other}))
	assert.Equal(t, []*models.StashBox{stashdb, tpdb}, identifyOrder([]*models.StashBox{stashdb, tpdb}))
	assert.Empty(t, identifyOrder(nil))
}
