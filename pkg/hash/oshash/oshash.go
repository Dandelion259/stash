// Package oshash implements the algorithm that OpenSubtitles.org uses to generate unique hashes.
//
// Calculation is as follows:
// size + 64 bit checksum of the first and last 64k bytes of the file.
package oshash

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
)

const chunkSize int64 = 64 * 1024

var ErrOsHashLen = errors.New("buffer is not a multiple of 8")

func sumBytes(buf []byte) (uint64, error) {
	if len(buf)%8 != 0 {
		return 0, ErrOsHashLen
	}

	sz := len(buf) / 8
	var sum uint64
	for j := 0; j < sz; j++ {
		sum += binary.LittleEndian.Uint64(buf[8*j : 8*(j+1)])
	}

	return sum, nil
}

func oshash(size int64, head []byte, tail []byte) (string, error) {
	headSum, err := sumBytes(head)
	if err != nil {
		return "", fmt.Errorf("oshash head: %w", err)
	}
	tailSum, err := sumBytes(tail)
	if err != nil {
		return "", fmt.Errorf("oshash tail: %w", err)
	}

	// Compute the sum of the head, tail and file size
	result := headSum + tailSum + uint64(size)
	// output as hex
	return fmt.Sprintf("%016x", result), nil
}

// FromReader calculates the hash reading from src.
func FromReader(src io.ReadSeeker, fileSize int64) (string, error) {
	if fileSize <= 8 {
		return "", fmt.Errorf("cannot calculate oshash where size < 8 (%d)", fileSize)
	}

	fileChunkSize := chunkSize
	if fileSize < fileChunkSize {
		// Must be a multiple of 8.
		fileChunkSize = (fileSize / 8) * 8
	}

	head := make([]byte, fileChunkSize)
	tail := make([]byte, fileChunkSize)

	// read the head of the file into the start of the buffer
	_, err := src.Read(head)
	if err != nil {
		return "", err
	}

	// seek to the end of the file - the chunk size
	_, err = src.Seek(-fileChunkSize, io.SeekEnd)
	if err != nil {
		return "", err
	}

	// read the tail of the file
	_, err = src.Read(tail)
	if err != nil {
		return "", err
	}

	return oshash(fileSize, head, tail)
}

// Is the equivalent of opening filePath, and calling FromReader with the data and file size.
func FromFilePath(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", err
	}

	fileSize := fi.Size()

	return FromReader(f, fileSize)
}

// rangeBytes fetches the absolute byte range [start, start+length-1] from url.
// Absolute offsets are used for BOTH head and tail (rather than the "bytes=-N"
// suffix form) because some WebDAV servers return an off-by-one body for suffix
// ranges, which would corrupt the oshash.
func rangeBytes(url string, start, length int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+length-1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("range request returned status %d (expected 206)", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, length))
}

// FromHTTP calculates the oshash of a remote file addressed by url, fetching only
// the first and last 64KB via Range requests (so the multi-GB body is never
// downloaded). fileSize must be the true content length (e.g. from ffprobe's
// format.size or a HEAD Content-Length). This yields the SAME oshash as hashing
// the file locally, so a .strm matches stash-boxes and dedups with the real file.
func FromHTTP(url string, fileSize int64) (string, error) {
	if fileSize <= 8 {
		return "", fmt.Errorf("cannot calculate oshash where size < 8 (%d)", fileSize)
	}

	fileChunkSize := chunkSize
	if fileSize < fileChunkSize {
		fileChunkSize = (fileSize / 8) * 8
	}

	head, err := rangeBytes(url, 0, fileChunkSize)
	if err != nil {
		return "", fmt.Errorf("oshash head range: %w", err)
	}
	// Absolute tail offset: [fileSize-chunk, fileSize-1].
	tail, err := rangeBytes(url, fileSize-fileChunkSize, fileChunkSize)
	if err != nil {
		return "", fmt.Errorf("oshash tail range: %w", err)
	}
	if int64(len(head)) != fileChunkSize || int64(len(tail)) != fileChunkSize {
		return "", fmt.Errorf("oshash short range read (head=%d tail=%d want=%d)", len(head), len(tail), fileChunkSize)
	}

	return oshash(fileSize, head, tail)
}

// httpContentLength issues a HEAD and returns the Content-Length, for callers
// that do not already know the remote size.
func httpContentLength(url string) (int64, error) {
	resp, err := http.Head(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	cl := resp.Header.Get("Content-Length")
	if cl == "" {
		return 0, errors.New("no Content-Length header")
	}
	return strconv.ParseInt(cl, 10, 64)
}

// FromHTTPAutoSize is FromHTTP but fetches the size via HEAD when not known.
func FromHTTPAutoSize(url string) (string, error) {
	size, err := httpContentLength(url)
	if err != nil {
		return "", fmt.Errorf("oshash size via HEAD: %w", err)
	}
	return FromHTTP(url, size)
}
