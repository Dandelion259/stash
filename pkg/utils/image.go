package utils

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Timeout to get the image. Includes transfer time. May want to make this
// configurable at some point.
const imageGetTimeout = time.Second * 60

// base64DataURI returns the data of a base64 data URI. It matches like
// `^data:.+\/(.+);base64,(.*)$` did, without a regexp: on an image of a few
// hundred KiB the regexp took 40-80 ms, and scene covers are processed inside
// the write transaction, where every other write waits for it.
func base64DataURI(s string) (string, bool) {
	const prefix, marker = "data:", ";base64,"
	// "." does not match a newline
	if !strings.HasPrefix(s, prefix) || strings.IndexByte(s, '\n') >= 0 {
		return "", false
	}
	i := strings.LastIndex(s, marker)
	if i < 0 {
		return "", false
	}
	// a slash with at least one character before and after it
	if strings.LastIndexByte(s[:i-1], '/') < len(prefix)+1 {
		return "", false
	}
	return s[i+len(marker):], true
}

// ProcessImageInput transforms an image string either from a base64 encoded
// string, or from a URL, and returns the image as a byte slice
func ProcessImageInput(ctx context.Context, imageInput string) ([]byte, error) {
	if imageInput == "" {
		return []byte{}, nil
	}

	if _, ok := base64DataURI(imageInput); ok {
		d, err := ProcessBase64Image(imageInput)
		return d, err
	}

	// assume input is a URL. Read it.
	d, err := ReadImageFromURL(ctx, imageInput)
	if err != nil {
		return nil, err
	}

	if err := validateImageData(d); err != nil {
		return nil, err
	}

	return d, nil
}

// validateImageData rejects HTML content, which is not a valid image and would
// execute as a document if served back to a browser. SVG (detected as XML or
// plain text) is still accepted and sandboxed on output by ServeImage.
func validateImageData(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	contentType := http.DetectContentType(data)
	if strings.HasPrefix(contentType, "text/html") {
		return fmt.Errorf("unsupported image content type %q", contentType)
	}

	return nil
}

// ReadImageFromURL returns image data from a URL
func ReadImageFromURL(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{
		Transport: &http.Transport{ // ignore insecure certificates
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			Proxy:           http.ProxyFromEnvironment,
		},

		Timeout: imageGetTimeout,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	// assume is a URL for now

	// set the host of the URL as the referer
	if req.URL.Scheme != "" {
		req.Header.Set("Referer", req.URL.Scheme+"://"+req.Host+"/")
	}
	req.Header.Set("User-Agent", getUserAgent())

	resp, err := client.Do(req)

	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http error %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

// ProcessBase64Image transforms a base64 encoded string from a form post and
// returns the image itself as a byte slice.
func ProcessBase64Image(imageString string) ([]byte, error) {
	if imageString == "" {
		return nil, fmt.Errorf("empty image string")
	}

	encodedString, ok := base64DataURI(imageString)
	if !ok {
		encodedString = imageString
	}
	imageData, err := GetDataFromBase64String(encodedString)
	if err != nil {
		return nil, err
	}

	if err := validateImageData(imageData); err != nil {
		return nil, err
	}

	return imageData, nil
}

// GetDataFromBase64String returns the given base64 encoded string as a byte slice
func GetDataFromBase64String(encodedString string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(encodedString)
}

// GetBase64StringFromData returns the given byte slice as a base64 encoded string
func GetBase64StringFromData(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func ServeImage(w http.ResponseWriter, r *http.Request, image []byte) {
	contentType := http.DetectContentType(image)

	// SVG images are detected as XML or plain text; serve them as SVG so they
	// render. The sandboxing CSP below prevents any embedded script running.
	if contentType == "text/xml; charset=utf-8" || contentType == "text/plain; charset=utf-8" {
		contentType = "image/svg+xml"
	} else if strings.HasPrefix(contentType, "text/") {
		// any other text type (e.g. HTML) is not a valid image - never render it
		contentType = "application/octet-stream"
		w.Header().Set("Content-Disposition", "attachment")
	}

	// sandbox every image response so a stored SVG cannot execute script or
	// exfiltrate data; harmless for raster images.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	w.Header().Set("Content-Type", contentType)
	ServeStaticContent(w, r, image)
}
