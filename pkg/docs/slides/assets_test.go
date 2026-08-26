package slides

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAssetIngestorRejectsPrivateAndUnsafeSchemes(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "http://127.0.0.1/x", "http://[::1]/x"} {
		if _, err := (AssetIngestor{}).Fetch(context.Background(), u); err == nil {
			t.Fatalf("expected %s rejected", u)
		}
	}
}

func TestAssetIngestorValidatesRedirectTarget(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://127.0.0.1/private"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	if _, err := (AssetIngestor{Transport: transport}).Fetch(context.Background(), "https://example.com/image.png"); err == nil {
		t.Fatal("expected private redirect target rejection")
	}
}

func TestAssetIngestorEnforcesMIMEAndSizeAndHashesBytes(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 16))
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader(string(png))),
			Request:    req,
		}, nil
	})
	asset, err := (AssetIngestor{Transport: transport}).Fetch(context.Background(), "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(png)
	if asset.ID != hex.EncodeToString(sum[:]) || asset.MIME != "image/png" {
		t.Fatalf("unexpected asset: %#v", asset)
	}
	if _, err := (AssetIngestor{Transport: transport, MaxBytes: 4}).Fetch(context.Background(), "https://example.com/image.png"); err == nil {
		t.Fatal("expected size rejection")
	}

	spoofed := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader("plain text")),
			Request:    req,
		}, nil
	})
	if _, err := (AssetIngestor{Transport: spoofed}).Fetch(context.Background(), "https://example.com/image.png"); err == nil {
		t.Fatal("expected MIME spoofing rejection")
	}
}

func TestValidateSVG(t *testing.T) {
	unsafe := []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image onerror="alert(1)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><animate onbegin="alert(1)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><use href="https://example.com/x.svg#id"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect fill="url(https://example.com/x)"/></svg>`,
		`<svg><rect></svg>`,
	}
	for _, input := range unsafe {
		if err := validateSVG([]byte(input)); err == nil {
			t.Fatalf("expected unsafe SVG rejection: %s", input)
		}
	}
	if err := validateSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><defs><path id="p"/></defs><use href="#p"/></svg>`)); err != nil {
		t.Fatal(err)
	}
}
