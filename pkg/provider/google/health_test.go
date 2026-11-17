package google

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/provider/streaming"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

const nativeTestResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`

func nativeTestProvider(t *testing.T) model.LLM {
	t.Helper()
	// Isolate backend selection from developer credentials. No real API calls.
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "false")
	t.Setenv("GOOGLE_GENAI_USE_ENTERPRISE", "false")
	p, err := NewProvider(context.Background(), "gemini-test", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func nativeTestRequest(endpoint string) *model.LLMRequest {
	return &model.LLMRequest{
		Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "test"}}}},
		Config:   &genai.GenerateContentConfig{HTTPOptions: &genai.HTTPOptions{BaseURL: endpoint}},
	}
}

func TestNativeSSEBodyPreservesDataAndDropsOnlyComments(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"comments", ": ping\r\n\r\ndata: {\"text\":\": retained\"}\n\n: trailing", "\r\ndata: {\"text\":\": retained\"}\n\n"},
		{"long-comment", ":" + strings.Repeat("x", 10000) + "\n" + "data: ok\n", "data: ok\n"},
		{"long-data", "data: " + strings.Repeat("x", 10000), "data: " + strings.Repeat("x", 10000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := io.NopCloser(strings.NewReader(tc.input))
			body := &nativeSSEBody{ReadCloser: raw, reader: bufio.NewReader(raw), lineStart: true}
			var got strings.Builder
			buf := make([]byte, 7)
			for {
				n, err := body.Read(buf)
				got.Write(buf[:n])
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if got.String() != tc.want {
				t.Fatalf("filtered bytes differ: got length %d, want %d", got.Len(), len(tc.want))
			}
		})
	}
}

func TestNativeGoogleSSECommentsAreRealActivity(t *testing.T) {
	p := nativeTestProvider(t)
	const idle = 150 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Error("API key authentication was lost")
		}
		if r.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("unexpected native endpoint: %s", r.URL)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		for i := 0; i < 16; i++ {
			if _, err := fmt.Fprint(w, ": upstream keepalive\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(25 * time.Millisecond):
			}
		}
		fmt.Fprintf(w, "data: %s\n\n", nativeTestResponse)
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()
	parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx, stop := streaming.StartHealthMonitor(parent, streaming.HealthOptions{IdleTimeout: idle, CheckInterval: 10 * time.Millisecond})
	defer stop()
	started := time.Now()
	var gotText bool
	for resp, err := range p.GenerateContent(ctx, nativeTestRequest(srv.URL), true) {
		if err != nil {
			t.Fatalf("active native SSE failed: %v (cause %v)", err, context.Cause(ctx))
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				gotText = gotText || part.Text == "hello"
			}
		}
	}
	if err := stop(); err != nil || !gotText || time.Since(started) < 2*idle {
		t.Fatalf("text=%v elapsed=%v health=%v", gotText, time.Since(started), err)
	}
}

func TestNativeGoogleSilenceAndParentCancellation(t *testing.T) {
	p := nativeTestProvider(t)
	for _, tc := range []struct {
		name                          string
		stream, headers, parentCancel bool
	}{
		{name: "stream-before-headers", stream: true},
		{name: "stream-after-headers", stream: true, headers: true},
		{name: "buffered-before-headers"},
		{name: "parent-cancel", stream: true, headers: true, parentCancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				if tc.headers {
					w.Header().Set("Content-Type", "text/event-stream")
					w.(http.Flusher).Flush()
				}
				if tc.parentCancel {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer srv.Close()
			ctx, stop := streaming.StartHealthMonitor(parent, streaming.HealthOptions{IdleTimeout: 100 * time.Millisecond, CheckInterval: 10 * time.Millisecond})
			defer stop()
			var requestErr error
			for _, err := range p.GenerateContent(ctx, nativeTestRequest(srv.URL), tc.stream) {
				if err != nil {
					requestErr = err
				}
			}
			cause := stop()
			if requestErr == nil {
				t.Fatal("silent request unexpectedly succeeded")
			}
			if tc.parentCancel {
				if !errors.Is(cause, context.Canceled) {
					t.Fatalf("cause = %v", cause)
				}
			} else {
				var stalled *streaming.GenerationStalledError
				if !errors.As(cause, &stalled) {
					t.Fatalf("cause = %v, want factual inactivity", cause)
				}
			}
		})
	}
}

func TestNativeGoogleOrdinaryRequestAndExplicitTimeout(t *testing.T) {
	p := nativeTestProvider(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Error("missing API key")
		}
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, nativeTestResponse)
	}))
	defer srv.Close()
	for resp, err := range p.GenerateContent(context.Background(), nativeTestRequest(srv.URL), false) {
		if err != nil || resp == nil || resp.Content == nil || resp.Content.Parts[0].Text != "hello" {
			t.Fatalf("ordinary request response=%v error=%v", resp, err)
		}
	}
	ctx, stop := streaming.StartHealthMonitor(context.Background(), streaming.HealthOptions{IdleTimeout: time.Second})
	defer stop()
	req := nativeTestRequest(srv.URL)
	timeout := 30 * time.Millisecond
	req.Config.HTTPOptions.Timeout = &timeout
	var requestErr error
	for _, err := range p.GenerateContent(ctx, req, true) {
		requestErr = err
	}
	if !errors.Is(requestErr, context.DeadlineExceeded) {
		t.Fatalf("explicit SDK timeout not preserved: %v", requestErr)
	}
	if cause := stop(); cause != nil {
		t.Fatalf("SDK timeout incorrectly attributed to watchdog: %v", cause)
	}
}
