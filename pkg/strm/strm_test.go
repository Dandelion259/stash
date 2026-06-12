package strm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsStrmPath(t *testing.T) {
	cases := map[string]bool{
		"/a/b/movie.strm": true,
		"/a/b/movie.STRM": true,
		"/a/b/movie.mp4":  false,
		"movie.strm":      true,
		"strm":            false,
		"/a/b/no.ext.mkv": false,
	}
	for in, want := range cases {
		if got := IsStrmPath(in); got != want {
			t.Errorf("IsStrmPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsRemoteURL(t *testing.T) {
	cases := map[string]bool{
		"http://h/v.mkv":  true,
		"https://h/v.mkv": true,
		"/local/v.mkv":    false,
		"ftp://h/v.mkv":   false,
	}
	for in, want := range cases {
		if got := IsRemoteURL(in); got != want {
			t.Errorf("IsRemoteURL(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestReadURLAndResolve(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "movie.strm")
	contents := "# comment line\n\nhttp://altmount:8080/movie.mkv\nignored second line\n"
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	url, err := ReadURL(p)
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://altmount:8080/movie.mkv" {
		t.Errorf("ReadURL = %q, want the first non-comment line", url)
	}

	if got := Resolve(p); got != url {
		t.Errorf("Resolve(strm) = %q, want %q", got, url)
	}

	// Non-strm paths pass through unchanged.
	if got := Resolve("/local/v.mkv"); got != "/local/v.mkv" {
		t.Errorf("Resolve(local) = %q, want passthrough", got)
	}
}
