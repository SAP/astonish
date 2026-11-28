package tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type publicFetchRoundTrip func(*http.Request) (*http.Response, error)

func (f publicFetchRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBotPublicFetchTransportBoundary(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, mode := range []string{"closed", "success", "private", "credential", "mode", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			http.DefaultTransport = publicFetchRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if mode == "cancel" {
					return nil, context.Canceled
				}
				if mode == "closed" {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("Evidence")), Request: r}, nil
			})
			args := WebFetchArgs{URL: "https://192.0.2.1/source"}
			switch mode {
			case "private":
				args.URL = "http://127.0.0.1/source"
			case "credential":
				args.URL = "https://user:password@192.0.2.1/source"
			case "mode":
				args.Mode = "invalid"
			}
			result, err := ExecuteTool(context.Background(), BotPublicFetchOperation, map[string]any{"url": args.URL, "mode": args.Mode}, "test")
			if mode == "closed" || mode == "success" {
				if err != nil || !result.(BotPublicFetchResult).ReadExecuted || calls != 1 {
					t.Fatalf("missing execution feedback: %+v %v calls=%d", result, err, calls)
				}
			} else {
				if err == nil {
					t.Fatal("native boundary error swallowed")
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if mode != "cancel" && calls != 0 {
					t.Fatal("rejected request dispatched")
				}
			}
		})
	}
}

func TestBotPublicFetchContextCancellation(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, mode := range []string{"before", "during_failure", "during_success"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			http.DefaultTransport = publicFetchRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				cancel()
				if mode == "during_failure" {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("Evidence")), Request: r}, nil
			})
			if mode == "before" {
				cancel()
			}
			result, err := ExecuteTool(ctx, BotPublicFetchOperation, map[string]any{"url": "https://192.0.2.1/source"}, "test")
			if !errors.Is(err, context.Canceled) || result.(BotPublicFetchResult).ReadExecuted {
				t.Fatalf("cancellation acquired read receipt: %+v %v", result, err)
			}
			if mode == "before" && calls != 0 {
				t.Fatal("cancelled request dispatched")
			}
		})
	}
}
