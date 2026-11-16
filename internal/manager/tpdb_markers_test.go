package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeTPDBMarkers(t *testing.T) {
	got := mergeTPDBMarkers([]tpdbMarker{
		{Title: "Cowgirl", Seconds: 620},
		{Title: "Intro", Seconds: 0},
		{Title: "Blowjob", Seconds: 300},
		// within 15 s of Blowjob: merged into it, as an extra tag
		{Title: "Handjob", Seconds: 310},
		// within 15 s of Handjob, not of Blowjob: still the same group
		{Title: "Titjob", Seconds: 322},
		{Title: "blowjob", Seconds: 305},
		// exactly 15 s after Cowgirl: a marker of its own
		{Title: "Doggy", Seconds: 635},
		// at 0 s: left out, and so is what was merged into it
		{Title: "Tease", Seconds: 9},
	})

	assert.Equal(t, []tpdbMarker{
		{Title: "Blowjob", Seconds: 300, Tags: []string{"Handjob", "Titjob"}},
		{Title: "Cowgirl", Seconds: 620},
		{Title: "Doggy", Seconds: 635},
	}, got)

	assert.Empty(t, mergeTPDBMarkers(nil))
}
