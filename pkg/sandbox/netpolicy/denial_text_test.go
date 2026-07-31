package netpolicy

import (
	"fmt"
	"testing"
)

func TestLooksLikeNetworkFailureText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "npm ECONNRESET socket hang up",
			text: "npm error code ECONNRESET\nnpm error network request to https://registry.npmjs.org/pkg failed, reason: socket hang up",
			want: true,
		},
		{
			name: "proxy denial",
			text: "curl: (56) CONNECT tunnel failed, response 403",
			want: true,
		},
		{
			name: "non-network process error",
			text: "SyntaxError: Unexpected token export",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksLikeNetworkFailureText(tt.text); got != tt.want {
				t.Fatalf("LooksLikeNetworkFailureText() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractDenialsFromText(t *testing.T) {
	text := "npm error network request to https://registry.npmjs.org/@upstash%2fcontext7-mcp failed, reason: socket hang up\n" +
		"dial tcp api.example.com:8443: connect: connection refused\n" +
		"> CONNECT packages.example.org:443 HTTP/1.1\r\n" +
		"curl: (6) Could not resolve host: mirror.example.net\n" +
		"retry https://registry.npmjs.org/another"

	denials := ExtractDenialsFromText(text)
	want := []string{
		"registry.npmjs.org:443",
		"api.example.com:8443",
		"packages.example.org:443",
		"mirror.example.net:443",
	}
	if len(denials) != len(want) {
		t.Fatalf("denials = %v, want %d entries", denials, len(want))
	}
	for i, expected := range want {
		got := denials[i]["host"].(string) + ":" + fmt.Sprint(denials[i]["port"])
		if got != expected {
			t.Fatalf("denial[%d] = %q, want %q (all: %v)", i, got, expected, denials)
		}
	}
}
