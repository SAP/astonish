package slides

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxAssetBytes int64 = 20 << 20

type Asset struct {
	ID, MIME string
	Bytes    []byte
}
type AssetIngestor struct {
	Transport http.RoundTripper
	Resolver  *net.Resolver
	Timeout   time.Duration
	MaxBytes  int64
}

func (a AssetIngestor) Fetch(ctx context.Context, rawURL string) (Asset, error) {
	max := a.MaxBytes
	if max <= 0 {
		max = MaxAssetBytes
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	resolver := a.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	transport := a.Transport
	if transport == nil {
		transport = &http.Transport{Proxy: nil, DisableCompression: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !publicIP(ip.IP) {
					return nil, fmt.Errorf("asset host resolves to a non-public address")
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("asset host has no addresses")
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		}}
	}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return validateAssetURL(req.Context(), resolver, req.URL)
	}}
	u, err := url.Parse(rawURL)
	if err != nil {
		return Asset{}, err
	}
	if err := validateAssetURL(ctx, resolver, u); err != nil {
		return Asset{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Asset{}, err
	}
	req.Header = make(http.Header)
	resp, err := client.Do(req)
	if err != nil {
		return Asset{}, fmt.Errorf("fetch asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Asset{}, fmt.Errorf("fetch asset: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return Asset{}, err
	}
	if int64(len(body)) > max {
		return Asset{}, fmt.Errorf("asset exceeds %d bytes", max)
	}
	detected := http.DetectContentType(body)
	declared, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if declared != "" && !compatibleMIME(declared, detected) {
		return Asset{}, fmt.Errorf("asset MIME mismatch: declared %s, detected %s", declared, detected)
	}
	if detected == "image/svg+xml" || declared == "image/svg+xml" {
		if err := validateSVG(body); err != nil {
			return Asset{}, err
		}
		detected = "image/svg+xml"
	}
	if !strings.HasPrefix(detected, "image/") {
		return Asset{}, fmt.Errorf("unsupported asset MIME %s", detected)
	}
	sum := sha256.Sum256(body)
	return Asset{ID: hex.EncodeToString(sum[:]), MIME: detected, Bytes: body}, nil
}
func validateAssetURL(ctx context.Context, r *net.Resolver, u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported asset URL scheme")
	}
	if u.User != nil {
		return fmt.Errorf("asset URL credentials are forbidden")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("asset URL host is required")
	}
	ips, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return fmt.Errorf("asset host resolves to a non-public address")
		}
	}
	return nil
}
func publicIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}
func compatibleMIME(declared, detected string) bool {
	if declared == detected {
		return true
	}
	return declared == "image/svg+xml" && strings.Contains(strings.ToLower(detected), "text")
}
func validateSVG(b []byte) error {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	dec.Strict = true
	seenRoot := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid SVG: %w", err)
		}
		switch v := tok.(type) {
		case xml.Directive:
			return fmt.Errorf("unsafe SVG directive")
		case xml.StartElement:
			name := strings.ToLower(v.Name.Local)
			if !seenRoot {
				if name != "svg" {
					return fmt.Errorf("invalid SVG root")
				}
				seenRoot = true
			}
			if name == "script" || name == "foreignobject" || name == "style" || name == "iframe" || name == "audio" || name == "video" {
				return fmt.Errorf("unsafe SVG element %s", name)
			}
			for _, attr := range v.Attr {
				key := strings.ToLower(attr.Name.Local)
				value := strings.TrimSpace(strings.ToLower(attr.Value))
				if strings.HasPrefix(key, "on") {
					return fmt.Errorf("unsafe SVG event attribute")
				}
				if key == "href" || key == "src" {
					if value != "" && !strings.HasPrefix(value, "#") && !strings.HasPrefix(value, "data:image/") {
						return fmt.Errorf("unsafe SVG external reference")
					}
				}
				if strings.Contains(value, "javascript:") || strings.Contains(value, "url(") || strings.Contains(value, "@import") {
					return fmt.Errorf("unsafe SVG attribute value")
				}
			}
		}
	}
	if !seenRoot {
		return fmt.Errorf("invalid SVG")
	}
	return nil
}
