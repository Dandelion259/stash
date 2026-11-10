package utils

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// a minimal valid 1x1 PNG
var pngImage = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestServeImage(t *testing.T) {
	const wantCSP = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; sandbox"

	tests := []struct {
		name            string
		image           []byte
		wantContentType string
		wantAttachment  bool
	}{
		{
			name:            "png is served as-is",
			image:           pngImage,
			wantContentType: "image/png",
		},
		{
			name:            "svg is served as image/svg+xml so it renders",
			image:           []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>`),
			wantContentType: "image/svg+xml",
		},
		{
			name:            "svg with xml prolog is served as image/svg+xml",
			image:           []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`),
			wantContentType: "image/svg+xml",
		},
		{
			name:            "html is never rendered - forced to an attachment",
			image:           []byte(`<!DOCTYPE html><html><script>alert(1)</script></html>`),
			wantContentType: "application/octet-stream",
			wantAttachment:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/image", nil)

			ServeImage(w, r, tt.image)

			if got := w.Header().Get("Content-Type"); got != tt.wantContentType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContentType)
			}
			if got := w.Header().Get("Content-Security-Policy"); got != wantCSP {
				t.Errorf("Content-Security-Policy = %q, want %q", got, wantCSP)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
			}
			gotAttachment := strings.Contains(w.Header().Get("Content-Disposition"), "attachment")
			if gotAttachment != tt.wantAttachment {
				t.Errorf("Content-Disposition attachment = %v, want %v (got %q)", gotAttachment, tt.wantAttachment, w.Header().Get("Content-Disposition"))
			}
		})
	}
}

func TestValidateImageData(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{name: "empty is allowed", data: nil, wantErr: false},
		{name: "png is allowed", data: pngImage, wantErr: false},
		{name: "svg is allowed", data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), wantErr: false},
		{name: "html is rejected", data: []byte(`<!DOCTYPE html><html></html>`), wantErr: true},
		{name: "html without doctype is rejected", data: []byte(`<html><body>x</body></html>`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateImageData(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateImageData() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBase64DataURI_matchesRegexp(t *testing.T) {
	re := regexp.MustCompile(`^data:.+\/(.+);base64,(.*)$`)
	big := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(make([]byte, 300*1024))
	inputs := []string{
		big,
		"data:image/png;base64,iVBORw0KGgo=",
		"data:image/svg+xml;base64,PHN2Zz4=",
		"data:text/plain; charset=utf-8;base64,aGk=",
		"data:image/png;base64,",
		"data:image/;base64,aGk=",
		"data:/png;base64,aGk=",
		"data:a/b;base64,aGk=",
		"data:/;base64,aGk=",
		"data:ab;base64,aGk=",
		"data:image/png;base64,a;base64,b",
		"data:image;base64,a/b;base64,c",
		"data:image/png;base64,aG\nk=",
		"data:image/png;base64",
		"data:image/png,aGk=",
		"DATA:image/png;base64,aGk=",
		"https://example.com/image.jpg",
		"https://example.com/data:image/png;base64,x",
		"",
		"data:",
	}
	for _, in := range inputs {
		name := in
		if len(name) > 60 {
			name = name[:60]
		}
		got, ok := base64DataURI(in)
		m := re.FindStringSubmatch(in)
		if ok != (m != nil) {
			t.Errorf("%q: match %v, regexp %v", name, ok, m != nil)
			continue
		}
		if ok && got != m[2] {
			t.Errorf("%q: data %q, regexp %q", name, got, m[2])
		}
	}
}

func TestProcessBase64Image_largeImageIsFast(t *testing.T) {
	img := append(append([]byte{}, pngImage...), make([]byte, 400*1024)...)
	in := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)

	start := time.Now()
	got, err := ProcessImageInput(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, img) {
		t.Fatal("decoded image differs")
	}
	// the regexp took 40-80 ms here, the decode takes about 1 ms
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Errorf("400 KiB data URI took %v", d)
	}
}
