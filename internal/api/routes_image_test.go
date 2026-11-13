package api

import (
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

func TestThumbnailSize(t *testing.T) {
	cases := []struct{ w, h, want int }{
		{2499, 1663, 624}, // 1/4 while decoding
		{1663, 2499, 624},
		{5000, 3333, 625}, // 1/8
		{1920, 1080, 640}, // 1/4 would be 480: too small, keep 640
		{640, 480, 640},
		{0, 0, 640}, // lazy zip picture, size not known yet
	}
	for _, c := range cases {
		f := &models.ImageFile{Width: c.w, Height: c.h}
		if got := thumbnailSize(f); got != c.want {
			t.Errorf("%dx%d: got %d, want %d", c.w, c.h, got, c.want)
		}
	}
	if got := thumbnailSize(&models.VideoFile{}); got != models.DefaultGthumbWidth {
		t.Errorf("video: got %d", got)
	}
}
