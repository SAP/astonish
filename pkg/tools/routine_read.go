package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// RoutineReadPolicy is supplied by trusted tenant composition, never tool arguments.
// Authorize must recheck the durable mandate and current network policy. Private
// addresses require a separate explicit policy decision, not a global SSRF toggle.
type RoutineReadPolicy struct {
	Authorize func(context.Context, string) error
	AllowIP   func(context.Context, string, net.IP) error
}
type routineReadKey struct{}

func WithRoutineReadPolicy(ctx context.Context, p RoutineReadPolicy) context.Context {
	return context.WithValue(ctx, routineReadKey{}, p)
}
func RoutineReadOrigin(raw string) (string, error) {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || strings.Contains(raw, "{{") {
		return "", fmt.Errorf("invalid routine read URL")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}

// ValidateRoutineRead rejects credentials, arbitrary headers, uploads and bodies,
// even on GET. The adapter is an unauthenticated retrieval capability only.
func ValidateRoutineRead(a HttpRequestArgs) error {
	m := strings.ToUpper(a.Method)
	if m != "" && m != http.MethodGet && m != http.MethodHead {
		return fmt.Errorf("routine read requires GET or HEAD")
	}
	if len(a.Headers) != 0 || a.Credential != "" || a.Body != "" || a.BodyPath != "" || len(a.Multipart) != 0 || a.ContentType != "" {
		return fmt.Errorf("routine read forbids credentials, headers and request bodies")
	}
	_, err := RoutineReadOrigin(a.URL)
	return err
}
func routineReadRequest(ctx context.Context, a HttpRequestArgs, p RoutineReadPolicy) (HttpRequestResult, error) {
	if err := ValidateRoutineRead(a); err != nil {
		return HttpRequestResult{}, err
	}
	if p.Authorize == nil {
		return HttpRequestResult{}, fmt.Errorf("routine read policy required")
	}
	authorize := func(raw string) error {
		origin, err := RoutineReadOrigin(raw)
		if err != nil {
			return err
		}
		return p.Authorize(ctx, origin)
	}
	if err := authorize(a.URL); err != nil {
		return HttpRequestResult{}, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(c context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(c, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("empty DNS answer")
		}
		for _, ip := range ips {
			if p.AllowIP != nil {
				if err := p.AllowIP(c, address, ip.IP); err != nil {
					return nil, err
				}
			} else if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
				return nil, fmt.Errorf("routine read private address denied")
			}
		}
		return (&net.Dialer{}).DialContext(c, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: httpReqDefaultTimeout, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > httpReqMaxRedirects {
			return fmt.Errorf("too many redirects")
		}
		return authorize(r.URL.String())
	}}
	method := strings.ToUpper(a.Method)
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, a.URL, nil)
	if err != nil {
		return HttpRequestResult{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return HttpRequestResult{}, err
	}
	defer res.Body.Close()
	limit := int64(httpReqDefaultMaxBytes)
	if a.MaxBytes > 0 && int64(a.MaxBytes) < limit {
		limit = int64(a.MaxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return HttpRequestResult{}, err
	}
	truncated := int64(len(body)) > limit
	if truncated {
		body = body[:limit]
	}
	return HttpRequestResult{FinalURL: res.Request.URL.String(), StatusCode: res.StatusCode, Status: res.Status, Body: string(body), ContentType: res.Header.Get("Content-Type"), Truncated: truncated}, nil
}
