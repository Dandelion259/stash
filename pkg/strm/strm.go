// Package strm provides helpers for .strm files: plain-text files whose
// contents are a single URL (or path) pointing at the real media. Stash keeps
// the .strm file on disk as the file's identity (for scanning, rename
// detection, fingerprinting) but uses the URL inside it as the input handed to
// ffmpeg/ffprobe and to the direct-stream handler.
package strm

import (
	"io"
	"net/http"
	"os"
	"strings"
)

// Extension is the file extension (without dot) used for .strm files.
const Extension = "strm"

// IsStrmPath reports whether path refers to a .strm file by extension.
func IsStrmPath(path string) bool {
	return strings.EqualFold(filepathExt(path), "."+Extension)
}

// filepathExt is a tiny inline of filepath.Ext to avoid importing path/filepath
// solely for one call; it returns the extension including the leading dot.
func filepathExt(path string) string {
	for i := len(path) - 1; i >= 0 && !os.IsPathSeparator(path[i]); i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}

// ReadURL reads the target URL out of a .strm file. The .strm format used by
// Kodi/Jellyfin is just the URL on a line; the first non-empty, non-comment
// line is returned. Comment lines start with '#'.
func ReadURL(strmPath string) (string, error) {
	data, err := os.ReadFile(strmPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line, nil
	}
	return "", nil
}

// IsRemoteURL reports whether s looks like an absolute URL ffmpeg can open
// directly (http/https). Local paths and bare filenames return false.
func IsRemoteURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// Resolve returns the input string that ffmpeg/ffprobe should use for the given
// on-disk path. For a .strm file it returns the URL inside it; for anything
// else it returns the path unchanged. If the .strm cannot be read, the original
// path is returned so the caller's existing error handling applies.
func Resolve(path string) string {
	if !IsStrmPath(path) {
		return path
	}
	url, err := ReadURL(path)
	if err != nil || url == "" {
		return path
	}
	return url
}

// hopHeaders are connection-specific headers that must not be forwarded when
// proxying.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding",
	"TE", "Trailer", "Upgrade",
}

// Proxy streams the origin URL back through the current response, forwarding the
// client's Range request and the origin's response (status, headers, body). Any
// credentials embedded in the URL stay server-side — the client only ever talks
// to stash. This is used for .strm playback instead of an HTTP redirect, because
// the origin (e.g. a docker-internal host, or a URL with user:pass@) is often
// not reachable or allowed by the browser directly.
func Proxy(w http.ResponseWriter, r *http.Request, url string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, "bad strm url", http.StatusBadGateway)
		return
	}

	// Forward range-related headers so seeking works.
	for _, h := range []string{"Range", "If-Range", "If-Modified-Since", "If-None-Match"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "strm origin error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy origin response headers (minus hop-by-hop) back to the client.
	for k, vv := range resp.Header {
		skip := false
		for _, hop := range hopHeaders {
			if strings.EqualFold(k, hop) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
