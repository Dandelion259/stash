// Package strm provides helpers for .strm files: plain-text files whose
// contents are a single URL (or path) pointing at the real media. Stash keeps
// the .strm file on disk as the file's identity (for scanning, rename
// detection, fingerprinting) but uses the URL inside it as the input handed to
// ffmpeg/ffprobe and to the direct-stream handler.
package strm

import (
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
