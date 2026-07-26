package api

import (
	"fmt"
	"testing"
)

func TestLooksLikeNetworkDenial(t *testing.T) {
	tests := []struct {
		name string
		resp map[string]any
		want bool
	}{
		// --- Strong indicators (trigger regardless of exit code) ---
		{
			name: "exit 0 + CONNECT tunnel failed (real-world proxy denial)",
			resp: map[string]any{
				"exit_code": float64(0),
				"stdout":    "* CONNECT tunnel: HTTP/1.1 negotiated\r\n* Establish HTTP proxy tunnel to identity-3.qa-de-1.cloud.sap:443\r\n> CONNECT identity-3.qa-de-1.cloud.sap:443 HTTP/1.1\r\r\n< HTTP/1.1 403 Forbidden\r\r\n< Content-Type: application/json\r\r\n< Content-Length: 101\r\r\n* CONNECT tunnel failed, response 403\r\ncurl: (56) CONNECT tunnel failed, response 403\r\n",
			},
			want: true,
		},
		{
			name: "exit 0 + egress denied",
			resp: map[string]any{
				"exit_code": float64(0),
				"stdout":    "Egress denied for host packages.sap.com:443\nFalling back...",
			},
			want: true,
		},
		{
			name: "exit 0 + egress blocked",
			resp: map[string]any{
				"exit_code": float64(0),
				"stdout":    "{\"error\":\"egress blocked\",\"host\":\"api.example.com\",\"port\":443}",
			},
			want: true,
		},
		{
			name: "exit 0 + policy violation",
			resp: map[string]any{
				"exit_code": float64(0),
				"stdout":    "Error: policy violation for outbound connection\nRetrying...\nDone.",
			},
			want: true,
		},
		{
			name: "exit 56 + CONNECT tunnel failed",
			resp: map[string]any{
				"exit_code": float64(56),
				"stdout":    "curl: (56) CONNECT tunnel failed, response 403",
			},
			want: true,
		},

		// --- Weak indicators (require non-zero exit code) ---
		{
			name: "exit 0 + connection refused is NOT a denial (weak indicator)",
			resp: map[string]any{
				"exit_code": float64(0),
				"stdout":    "Warning: connection refused, retrying\nSuccess on retry.",
			},
			want: false,
		},
		{
			name: "non-zero exit + connection refused",
			resp: map[string]any{
				"exit_code": float64(7),
				"stdout":    "curl: (7) Failed to connect to api.example.com port 443: Connection refused",
			},
			want: true,
		},
		{
			name: "non-zero exit + 403 forbidden",
			resp: map[string]any{
				"exit_code": float64(22),
				"stdout":    "HTTP/1.1 403 Forbidden\nContent-Length: 101",
			},
			want: true,
		},
		{
			name: "non-zero exit + could not resolve host",
			resp: map[string]any{
				"exit_code": float64(6),
				"stdout":    "curl: (6) Could not resolve host: packages.sap.com",
			},
			want: true,
		},
		{
			name: "non-zero exit + unrelated error",
			resp: map[string]any{
				"exit_code": float64(1),
				"stdout":    "file not found: /etc/config.yaml",
			},
			want: false,
		},
		{
			name: "non-zero exit + empty stdout",
			resp: map[string]any{
				"exit_code": float64(1),
				"stdout":    "",
			},
			want: false,
		},
		{
			name: "int exit_code (non-zero) + network error",
			resp: map[string]any{
				"exit_code": 1,
				"stdout":    "Failed to connect to api.sap.com:443",
			},
			want: true,
		},

		// --- Edge cases ---
		{
			name: "no exit_code field + connection refused",
			resp: map[string]any{
				"stdout": "connection refused",
			},
			want: false,
		},
		{
			name: "nil resp",
			resp: nil,
			want: false,
		},
		{
			name: "exit 0 + normal 403 from a web server (not proxy)",
			resp: map[string]any{
				"exit_code": float64(0),
				"stdout":    "HTTP/1.1 403 Forbidden\n{\"error\":\"unauthorized\"}",
			},
			want: false, // weak indicator "403 forbidden" requires non-zero exit
		},
		{
			name: "exit 0 + multi-line script with proxy content-length 101",
			resp: map[string]any{
				"exit_code": float64(0),
				"stdout":    "Payload written. Authenticating...\r\n< HTTP/1.1 403 Forbidden\r\r\n< Content-Type: application/json\r\r\n< Content-Length: 101\r\r\n* CONNECT tunnel failed, response 403\r\ncurl: (56) CONNECT tunnel failed, response 403\r\n--- Response file size ---\r\nFile not found\r\n",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeNetworkDenial(tt.resp)
			if got != tt.want {
				t.Errorf("looksLikeNetworkDenial() = %v, want %v\nresp: %v", got, tt.want, tt.resp)
			}
		})
	}
}

func TestLooksLikeShellNetworkFailure(t *testing.T) {
	tests := []struct {
		name string
		resp map[string]any
		want bool
	}{
		{
			name: "nonzero exit with HTTP status zero",
			resp: map[string]any{"exit_code": 7, "stdout": "\nHTTP_STATUS: 000\n"},
			want: true,
		},
		{
			name: "zero exit with HTTP status zero is not enough",
			resp: map[string]any{"exit_code": 0, "stdout": "\nHTTP_STATUS: 000\n"},
			want: false,
		},
		{
			name: "nonzero exit with curl failed to connect",
			resp: map[string]any{"exit_code": 7, "stderr": "curl: (7) Failed to connect to example.com port 443"},
			want: true,
		},
		{
			name: "nonzero exit with unrelated output",
			resp: map[string]any{"exit_code": 1, "stdout": "file not found"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeShellNetworkFailure(tt.resp)
			if got != tt.want {
				t.Fatalf("looksLikeShellNetworkFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsNetworkTool(t *testing.T) {
	yes := []string{"browser_navigate", "browser_tabs", "web_fetch", "http_request", "read_pdf"}
	no := []string{"shell_command", "write_file", "read_file", "grep_search", "memory_save", ""}

	for _, name := range yes {
		if !isNetworkTool(name) {
			t.Errorf("isNetworkTool(%q) = false, want true", name)
		}
	}
	for _, name := range no {
		if isNetworkTool(name) {
			t.Errorf("isNetworkTool(%q) = true, want false", name)
		}
	}
}

func TestLooksLikeNetworkToolDenial(t *testing.T) {
	tests := []struct {
		name string
		resp map[string]any
		want bool
	}{
		// --- Browser tools (Chrome errors via CDP/rod) ---
		{
			name: "browser_navigate: ERR_TUNNEL_CONNECTION_FAILED",
			resp: map[string]any{
				"error": "navigation failed: net::ERR_TUNNEL_CONNECTION_FAILED",
			},
			want: true,
		},
		{
			name: "browser_navigate: ERR_PROXY_CONNECTION_FAILED",
			resp: map[string]any{
				"error": "navigation failed: net::ERR_PROXY_CONNECTION_FAILED at https://api.example.com/path",
			},
			want: true,
		},
		{
			name: "browser_navigate: ERR_NAME_NOT_RESOLVED",
			resp: map[string]any{
				"error": "navigation failed: net::ERR_NAME_NOT_RESOLVED",
			},
			want: true,
		},

		// --- Go HTTP client errors (web_fetch, http_request, read_pdf) ---
		{
			name: "web_fetch: proxy CONNECT tunnel 403",
			resp: map[string]any{
				"error": `fetch failed: Get "https://api.sap.com/api/v1/data": proxyconnect tcp: dial tcp 10.200.0.1:3128: connect tunnel failed, response 403`,
			},
			want: true,
		},
		{
			name: "http_request: connection refused through proxy",
			resp: map[string]any{
				"error": `request failed: Get "https://packages.sap.com/repo": connection refused`,
			},
			want: true,
		},
		{
			name: "read_pdf: fetch failed with proxy denial",
			resp: map[string]any{
				"error": `failed to fetch PDF: Get "https://docs.example.com/report.pdf": proxyconnect tcp: CONNECT tunnel failed, response 403`,
			},
			want: true,
		},
		{
			name: "http_request: name not resolved",
			resp: map[string]any{
				"error": `request failed: Get "https://internal.corp.example.com/api": dial tcp: lookup internal.corp.example.com: could not resolve host: internal.corp.example.com`,
			},
			want: true,
		},
		{
			name: "web_fetch: proxy returns Forbidden (Go HTTP url.Error format)",
			resp: map[string]any{
				"error": `fetch failed: Get "https://www.globo.com": Forbidden`,
			},
			want: true,
		},
		{
			name: "web_fetch: proxy returns Bad Gateway",
			resp: map[string]any{
				"error": `fetch failed: Get "https://api.example.com/data": Bad Gateway`,
			},
			want: true,
		},
		{
			name: "http_request: proxy returns Service Unavailable",
			resp: map[string]any{
				"error": `request failed: Get "https://blocked.host.com/path": Service Unavailable`,
			},
			want: true,
		},

		// --- Negative cases ---
		{
			name: "no error field",
			resp: map[string]any{
				"url":    "https://api.sap.com",
				"status": 200,
			},
			want: false,
		},
		{
			name: "non-network error",
			resp: map[string]any{
				"error": "url is required",
			},
			want: false,
		},
		{
			name: "unrelated HTTP error (timeout)",
			resp: map[string]any{
				"error": "request failed: context deadline exceeded",
			},
			want: false,
		},
		{
			name: "nil resp",
			resp: nil,
			want: false,
		},
		{
			name: "empty error string",
			resp: map[string]any{
				"error": "",
			},
			want: false,
		},
		{
			name: "error as error type",
			resp: map[string]any{
				"error": fmt.Errorf("connection refused to api.sap.com:443"),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeNetworkToolDenial(tt.resp)
			if got != tt.want {
				t.Errorf("looksLikeNetworkToolDenial() = %v, want %v\nresp: %v", got, tt.want, tt.resp)
			}
		})
	}
}

func TestExtractDenialFromToolError(t *testing.T) {
	tests := []struct {
		name        string
		resp        map[string]any
		fallbackURL string
		wantHosts   []string
	}{
		{
			name: "URL in resp + dial error in error string",
			resp: map[string]any{
				"url":   "https://api.sap.com/v1/data",
				"error": `fetch failed: Get "https://api.sap.com/v1/data": dial tcp api.sap.com:443: connect tunnel failed`,
			},
			wantHosts: []string{"api.sap.com:443"},
		},
		{
			name: "only error string with quoted URL",
			resp: map[string]any{
				"error": `request failed: Get "https://packages.internal.corp.sap.com:8443/index": proxyconnect tcp: CONNECT tunnel failed`,
			},
			wantHosts: []string{"packages.internal.corp.sap.com:8443"},
		},
		{
			name: "only error string with dial tcp host:port",
			resp: map[string]any{
				"error": `navigation failed: dial tcp compute.cloud.sap:443: connection refused`,
			},
			wantHosts: []string{"compute.cloud.sap:443"},
		},
		{
			name: "URL field only, no host in error",
			resp: map[string]any{
				"url":   "https://globo.com/news",
				"error": "net::ERR_TUNNEL_CONNECTION_FAILED",
			},
			wantHosts: []string{"globo.com:443"},
		},
		{
			name: "HTTP URL defaults to port 80",
			resp: map[string]any{
				"url":   "http://example.com/path",
				"error": "connection refused",
			},
			wantHosts: []string{"example.com:80"},
		},
		{
			name: "no extractable host without fallback",
			resp: map[string]any{
				"error": "net::ERR_TUNNEL_CONNECTION_FAILED",
			},
			wantHosts: nil,
		},
		{
			name: "fallbackURL used when error has no host (Chrome generic error)",
			resp: map[string]any{
				"error": "navigation failed: net::ERR_TUNNEL_CONNECTION_FAILED",
			},
			fallbackURL: "https://www.globo.com/news",
			wantHosts:   []string{"www.globo.com:443"},
		},
		{
			name: "fallbackURL with custom port",
			resp: map[string]any{
				"error": "navigation failed: net::ERR_PROXY_CONNECTION_FAILED",
			},
			fallbackURL: "https://internal.sap.com:8443/api",
			wantHosts:   []string{"internal.sap.com:8443"},
		},
		{
			name: "resp URL takes priority over fallbackURL (dedup)",
			resp: map[string]any{
				"url":   "https://api.sap.com/v1",
				"error": "net::ERR_TUNNEL_CONNECTION_FAILED",
			},
			fallbackURL: "https://api.sap.com/v1",
			wantHosts:   []string{"api.sap.com:443"},
		},
		{
			name: "CONNECT pattern in error (same as shell stdout)",
			resp: map[string]any{
				"error": "CONNECT identity.cloud.sap:443 HTTP/1.1 returned 403",
			},
			wantHosts: []string{"identity.cloud.sap:443"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			denials := extractDenialFromToolError(tt.resp, tt.fallbackURL)

			if len(tt.wantHosts) == 0 {
				if len(denials) != 0 {
					t.Errorf("expected no denials, got %d: %v", len(denials), denials)
				}
				return
			}

			if len(denials) != len(tt.wantHosts) {
				t.Fatalf("expected %d denial(s), got %d: %v", len(tt.wantHosts), len(denials), denials)
			}

			for i, want := range tt.wantHosts {
				got := denials[i]["host"].(string) + ":" + fmt.Sprint(denials[i]["port"])
				if got != want {
					t.Errorf("denial[%d] = %q, want %q", i, got, want)
				}
				// Verify broader_pattern is present
				if bp, ok := denials[i]["broader_pattern"].(string); ok && bp != "" {
					t.Logf("denial[%d] broader_pattern = %q", i, bp)
				}
			}
		})
	}
}

func TestHostPortFromURL(t *testing.T) {
	tests := []struct {
		url      string
		wantHost string
		wantPort int
	}{
		{"https://api.sap.com/path", "api.sap.com", 443},
		{"http://example.com/path", "example.com", 80},
		{"https://internal.corp.sap.com:8443/index", "internal.corp.sap.com", 8443},
		{"http://localhost:3000/api", "localhost", 3000},
		{"not-a-url", "", 0},
		{"", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			host, port := hostPortFromURL(tt.url)
			if host != tt.wantHost || port != tt.wantPort {
				t.Errorf("hostPortFromURL(%q) = (%q, %d), want (%q, %d)",
					tt.url, host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestExtractDenialsFromShellCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantHosts []string
	}{
		{
			name:      "quoted https URL",
			command:   `curl -s "https://api.example.com/v1/resources"`,
			wantHosts: []string{"api.example.com:443"},
		},
		{
			name:      "multiple generic https URLs",
			command:   `curl -s "https://clusters.example.net/api"; curl -s 'https://identity.example.org/v3/auth/tokens'`,
			wantHosts: []string{"clusters.example.net:443", "identity.example.org:443"},
		},
		{
			name:      "http URL defaults to port 80",
			command:   `curl http://metadata.example.internal/latest`,
			wantHosts: []string{"metadata.example.internal:80"},
		},
		{
			name:      "custom port and trailing punctuation",
			command:   `python -c 'fetch("https://internal.example.com:8443/api");'`,
			wantHosts: []string{"internal.example.com:8443"},
		},
		{
			name:      "deduplicates repeated URL",
			command:   `curl https://api.example.com/a && curl https://api.example.com/b`,
			wantHosts: []string{"api.example.com:443"},
		},
		{
			name:      "non URL command ignored",
			command:   `curl api.example.com/path`,
			wantHosts: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			denials := extractDenialsFromShellCommand(tt.command)
			if len(tt.wantHosts) == 0 {
				if len(denials) != 0 {
					t.Fatalf("expected no denials, got %v", denials)
				}
				return
			}
			if len(denials) != len(tt.wantHosts) {
				t.Fatalf("expected %d denials, got %d: %v", len(tt.wantHosts), len(denials), denials)
			}
			for i, want := range tt.wantHosts {
				got := denials[i]["host"].(string) + ":" + fmt.Sprint(denials[i]["port"])
				if got != want {
					t.Fatalf("denial[%d] = %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestExtractDenialsFromOutput(t *testing.T) {
	tests := []struct {
		name      string
		stdout    string
		wantHosts []string // expected host:port pairs
	}{
		{
			name:      "real-world CONNECT tunnel denial",
			stdout:    "* Establish HTTP proxy tunnel to identity-3.qa-de-1.cloud.sap:443\r\n> CONNECT identity-3.qa-de-1.cloud.sap:443 HTTP/1.1\r\r\n< HTTP/1.1 403 Forbidden\r\r\n* CONNECT tunnel failed, response 403\r\n",
			wantHosts: []string{"identity-3.qa-de-1.cloud.sap:443"},
		},
		{
			name:      "multiple CONNECT denials deduplicated",
			stdout:    "> CONNECT api.sap.com:443 HTTP/1.1\r\n< 403\r\n> CONNECT api.sap.com:443 HTTP/1.1\r\n< 403\r\n",
			wantHosts: []string{"api.sap.com:443"},
		},
		{
			name:      "could not resolve host",
			stdout:    "curl: (6) Could not resolve host: packages.internal.sap.com\n",
			wantHosts: []string{"packages.internal.sap.com:443"},
		},
		{
			name:      "multiple different hosts",
			stdout:    "> CONNECT identity.cloud.sap:443 HTTP/1.1\r\n< 403\r\n> CONNECT compute.cloud.sap:8774 HTTP/1.1\r\n< 403\r\n",
			wantHosts: []string{"identity.cloud.sap:443", "compute.cloud.sap:8774"},
		},
		{
			name:      "no denials in normal output",
			stdout:    "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n{\"servers\": []}",
			wantHosts: nil,
		},
		{
			name:      "empty stdout",
			stdout:    "",
			wantHosts: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			denials := extractDenialsFromOutput(tt.stdout)

			if len(tt.wantHosts) == 0 {
				if len(denials) != 0 {
					t.Errorf("expected no denials, got %d: %v", len(denials), denials)
				}
				return
			}

			if len(denials) != len(tt.wantHosts) {
				t.Fatalf("expected %d denials, got %d: %v", len(tt.wantHosts), len(denials), denials)
			}

			for i, want := range tt.wantHosts {
				got := denials[i]["host"].(string) + ":" + fmt.Sprint(denials[i]["port"])
				if got != want {
					t.Errorf("denial[%d] = %q, want %q", i, got, want)
				}
				// Verify broader_pattern is present for multi-label hosts
				if bp, ok := denials[i]["broader_pattern"].(string); ok && bp != "" {
					t.Logf("denial[%d] broader_pattern = %q", i, bp)
				}
			}
		})
	}
}
