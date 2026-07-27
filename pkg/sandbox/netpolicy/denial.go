package netpolicy

import (
	"fmt"
	nurl "net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox/openshell"
)

// LooksLikeNetworkDenial checks a shell_command response for L7 proxy denial.
func LooksLikeNetworkDenial(resp map[string]any) bool {
	stdout, _ := resp["stdout"].(string)
	if stdout == "" {
		return false
	}
	lower := strings.ToLower(stdout)
	for _, indicator := range proxyDenialIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	if !hasNonZeroExitCode(resp) {
		return false
	}
	for _, indicator := range networkErrorIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}

func hasNonZeroExitCode(resp map[string]any) bool {
	exitCode, ok := resp["exit_code"]
	if !ok {
		return false
	}
	switch v := exitCode.(type) {
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

var proxyDenialIndicators = []string{
	"connect tunnel failed, response 403",
	"connect tunnel failed",
	"proxy returned 403",
	"proxy refused connection",
	"egress denied",
	"egress blocked",
	"policy violation",
	"http/1.1 403 forbidden\r\r\n< content-type: application/json\r\r\n< content-length: 101",
}

var networkErrorIndicators = []string{
	"connection refused",
	"connection reset",
	"connection timed out",
	"network is unreachable",
	"no route to host",
	"403 forbidden",
	"ssl_error_handshake",
	"could not resolve host",
	"name or service not known",
	"failed to connect",
}

var shellStatusZeroIndicators = []string{
	"http_status: 000",
	"http status: 000",
}

var shellNetworkFailureIndicators = []string{
	"curl: (5)",
	"curl: (6)",
	"curl: (7)",
	"curl: (28)",
	"curl: (35)",
	"curl: (52)",
	"curl: (56)",
	"failed to connect",
	"connection timed out",
	"could not resolve host",
	"connect tunnel failed",
	"proxy returned 403",
	"proxy refused connection",
}

// LooksLikeShellNetworkFailure checks generic shell/curl failures that may not
// include proxy details but can be mapped from URLs in the original command.
func LooksLikeShellNetworkFailure(resp map[string]any) bool {
	if resp == nil {
		return false
	}
	text := responseText(resp)
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, indicator := range shellStatusZeroIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	if !hasNonZeroExitCode(resp) {
		return false
	}
	for _, indicator := range shellNetworkFailureIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}

func responseText(resp map[string]any) string {
	if resp == nil {
		return ""
	}
	var parts []string
	for _, key := range []string{"stdout", "stderr", "error"} {
		switch v := resp[key].(type) {
		case string:
			parts = append(parts, v)
		case error:
			parts = append(parts, v.Error())
		}
	}
	return strings.Join(parts, "\n")
}

// ExtractDenialsFromOutput parses tool stdout for denied host:port pairs.
func ExtractDenialsFromOutput(stdout string) []map[string]any {
	seen := make(map[string]bool)
	var denials []map[string]any

	add := func(host string, port int) {
		key := fmt.Sprintf("%s:%d", host, port)
		if seen[key] {
			return
		}
		seen[key] = true
		denials = append(denials, map[string]any{
			"host":            host,
			"port":            port,
			"broader_pattern": openshell.SuggestBroaderPattern(host),
		})
	}

	for _, match := range connectHostPattern.FindAllStringSubmatch(stdout, -1) {
		port := 443
		if match[2] != "" {
			if p, err := strconv.Atoi(match[2]); err == nil {
				port = p
			}
		}
		add(match[1], port)
	}
	for _, match := range tunnelToPattern.FindAllStringSubmatch(stdout, -1) {
		port := 443
		if match[2] != "" {
			if p, err := strconv.Atoi(match[2]); err == nil {
				port = p
			}
		}
		add(match[1], port)
	}
	for _, match := range resolveFailPattern.FindAllStringSubmatch(stdout, -1) {
		add(match[1], 443)
	}
	return denials
}

var (
	connectHostPattern = regexp.MustCompile(`CONNECT\s+([a-zA-Z0-9._-]+):(\d+)\s+HTTP/`)
	tunnelToPattern    = regexp.MustCompile(`tunnel to ([a-zA-Z0-9._-]+):(\d+)`)
	resolveFailPattern = regexp.MustCompile(`(?i)could not resolve host:\s*([a-zA-Z0-9._-]+)`)
	goHTTPHostPattern  = regexp.MustCompile(`dial tcp ([a-zA-Z0-9._-]+):(\d+)`)
	urlInErrorPattern  = regexp.MustCompile(`"(https?://[^"]+)"`)
	shellURLPattern    = regexp.MustCompile(`https?://[^\s"'<>\\]+`)
)

var networkToolNames = map[string]bool{
	"browser_navigate": true,
	"browser_tabs":     true,
	"web_fetch":        true,
	"http_request":     true,
	"read_pdf":         true,
}

// IsNetworkTool reports whether the tool makes external HTTP requests.
func IsNetworkTool(name string) bool {
	return networkToolNames[name]
}

// LooksLikeNetworkToolDenial checks non-shell tool errors for proxy denials.
func LooksLikeNetworkToolDenial(resp map[string]any) bool {
	errVal, ok := resp["error"]
	if !ok || errVal == nil {
		return false
	}
	var errStr string
	switch v := errVal.(type) {
	case string:
		errStr = v
	case error:
		errStr = v.Error()
	default:
		return false
	}
	if errStr == "" {
		return false
	}
	lower := strings.ToLower(errStr)
	for _, indicator := range proxyDenialIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	for _, indicator := range chromeNetworkDenialIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	for _, indicator := range goHTTPProxyDenialIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	for _, indicator := range networkErrorIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}

var chromeNetworkDenialIndicators = []string{
	"net::err_tunnel_connection_failed",
	"net::err_proxy_connection_failed",
	"net::err_connection_refused",
	"net::err_connection_reset",
	"net::err_connection_closed",
	"net::err_name_not_resolved",
	"net::err_proxy_auth_unsupported",
}

var goHTTPProxyDenialIndicators = []string{
	"\": forbidden",
	"\": proxy authentication required",
	"\": service unavailable",
	"\": bad gateway",
}

// ExtractDenialsFromShellCommand extracts denied hosts from URLs embedded in a
// shell command. It intentionally handles only explicit http/https URLs to
// avoid false positives from arbitrary command arguments or filenames.
func ExtractDenialsFromShellCommand(command string) []map[string]any {
	seen := make(map[string]bool)
	var denials []map[string]any

	add := func(host string, port int) {
		if host == "" {
			return
		}
		key := fmt.Sprintf("%s:%d", host, port)
		if seen[key] {
			return
		}
		seen[key] = true
		denials = append(denials, map[string]any{
			"host":            host,
			"port":            port,
			"broader_pattern": openshell.SuggestBroaderPattern(host),
		})
	}

	for _, raw := range shellURLPattern.FindAllString(command, -1) {
		urlStr := strings.TrimRight(raw, ".,;:)]}")
		if host, port := HostPortFromURL(urlStr); host != "" {
			add(host, port)
		}
	}
	return denials
}

// ExtractDenialFromToolError extracts denied hosts from a network tool response.
func ExtractDenialFromToolError(resp map[string]any, fallbackURL string) []map[string]any {
	seen := make(map[string]bool)
	var denials []map[string]any

	add := func(host string, port int) {
		if host == "" {
			return
		}
		k := fmt.Sprintf("%s:%d", host, port)
		if seen[k] {
			return
		}
		seen[k] = true
		denials = append(denials, map[string]any{
			"host":            host,
			"port":            port,
			"broader_pattern": openshell.SuggestBroaderPattern(host),
		})
	}

	for _, key := range []string{"url", "URL", "final_url"} {
		if urlStr, ok := resp[key].(string); ok && urlStr != "" {
			if host, port := HostPortFromURL(urlStr); host != "" {
				add(host, port)
			}
		}
	}
	if fallbackURL != "" {
		if host, port := HostPortFromURL(fallbackURL); host != "" {
			add(host, port)
		}
	}

	errStr := ""
	if v, ok := resp["error"].(string); ok {
		errStr = v
	}
	if errStr != "" {
		for _, match := range goHTTPHostPattern.FindAllStringSubmatch(errStr, -1) {
			port := 443
			if match[2] != "" {
				if p, err := strconv.Atoi(match[2]); err == nil {
					port = p
				}
			}
			add(match[1], port)
		}
		for _, match := range urlInErrorPattern.FindAllStringSubmatch(errStr, -1) {
			if host, port := HostPortFromURL(match[1]); host != "" {
				add(host, port)
			}
		}
		for _, match := range connectHostPattern.FindAllStringSubmatch(errStr, -1) {
			port := 443
			if match[2] != "" {
				if p, err := strconv.Atoi(match[2]); err == nil {
					port = p
				}
			}
			add(match[1], port)
		}
	}
	return denials
}

// HostPortFromURL parses a URL and returns (host, port).
func HostPortFromURL(rawURL string) (string, int) {
	parsed, err := nurl.Parse(rawURL)
	if err != nil {
		return "", 0
	}
	host := parsed.Hostname()
	if host == "" {
		return "", 0
	}
	port := 443
	if parsed.Scheme == "http" {
		port = 80
	}
	if parsed.Port() != "" {
		if p, err := strconv.Atoi(parsed.Port()); err == nil {
			port = p
		}
	}
	return host, port
}
