package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ConfiguredSearchBackend is a trusted, current scoped configuration, not tool
// input. Only the OpenAI-compatible chat-completions search protocol is supported.
// Credential is a vault reference; it is never an environment variable or key.
type ConfiguredSearchBackend struct {
	Provider   string
	Model      string
	Endpoint   string
	Credential string
}

func (b ConfiguredSearchBackend) Validate() error {
	u, err := url.Parse(b.Endpoint)
	if err != nil || u == nil || u.RawQuery != "" || b.Provider == "" || b.Model == "" || b.Credential == "" {
		return fmt.Errorf("invalid configured search backend")
	}
	if u.Scheme != "https" {
		// Only explicit loopback test servers may use cleartext transport.
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("search backend requires HTTPS")
		}
	}
	_, err = RoutineReadOrigin(b.Endpoint)
	return err
}

// RunConfiguredSearch uses only the explicitly supplied vault credential and
// transport policy. It never invokes a general LLM factory, proxy, host config,
// OAuth refresh, tool execution, or redirects (including same-origin redirects).
func RunConfiguredSearch(ctx context.Context, query string, b ConfiguredSearchBackend, token string, p RoutineReadPolicy) (PerplexityWebSearchResult, error) {
	var result PerplexityWebSearchResult
	if b.Validate() != nil || strings.TrimSpace(query) == "" || len(query) > 2048 || strings.Contains(query, "{{") || token == "" || strings.ContainsAny(token, "\r\n") || p.Authorize == nil {
		return result, fmt.Errorf("configured search denied")
	}
	origin, _ := RoutineReadOrigin(b.Endpoint)
	if err := p.Authorize(ctx, origin); err != nil {
		return result, err
	}
	tr := &http.Transport{TLSHandshakeTimeout: httpReqDefaultTimeout, ResponseHeaderTimeout: httpReqDefaultTimeout}
	tr.DialContext = func(c context.Context, network, address string) (net.Conn, error) {
		if err := p.Authorize(c, origin); err != nil {
			return nil, err
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(c, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("empty search DNS answer")
		}
		for _, ip := range ips {
			if p.AllowIP != nil {
				if err := p.AllowIP(c, address, ip.IP); err != nil {
					return nil, err
				}
			} else if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
				return nil, fmt.Errorf("private search address denied")
			}
		}
		return (&net.Dialer{}).DialContext(c, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	defer tr.CloseIdleConnections()
	body, _ := json.Marshal(map[string]any{"model": b.Model, "stream": false, "max_tokens": 4096, "messages": []map[string]string{{"role": "user", "content": buildPerplexitySearchPrompt(PerplexityWebSearchArgs{Query: query}, "medium", 5)}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.Endpoint, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Transport: tr, Timeout: httpReqDefaultTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("search redirects forbidden") }}
	res, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("configured search transport failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return result, fmt.Errorf("configured search status %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return result, fmt.Errorf("configured search response too large or unreadable")
	}
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Citations []string       `json:"citations"`
		Usage     map[string]any `json:"usage"`
	}
	if json.Unmarshal(raw, &reply) != nil || len(reply.Choices) == 0 {
		return result, fmt.Errorf("invalid configured search response")
	}
	result = PerplexityWebSearchResult{Query: query, Provider: b.Provider, Model: b.Model, Answer: reply.Choices[0].Message.Content}
	result.Citations, result.SearchResults = extractSources(result.Answer, nil)
	for _, citation := range reply.Citations {
		if _, err := RoutineReadOrigin(citation); err == nil {
			result.Citations = append(result.Citations, citation)
			result.SearchResults = append(result.SearchResults, PerplexitySearchHit{URL: citation})
		}
	}
	if len(result.Citations) == 0 {
		result.Warning = "Search returned no verifiable citations."
	}
	if reply.Usage != nil {
		result.Metadata = map[string]interface{}{"usage_metadata": reply.Usage}
	}
	return result, nil
}
