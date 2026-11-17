package httpool

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/SAP/astonish/pkg/provider/streaming"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHealthPoolSelectionAndTimeouts(t *testing.T) {
	if generationTransport == sharedTransport || generationTransport.ResponseHeaderTimeout != 0 || sharedTransport.ResponseHeaderTimeout != 120*time.Second {
		t.Fatal("header timeout pools")
	}
	if generationTransport.TLSHandshakeTimeout != sharedTransport.TLSHandshakeTimeout || generationTransport.DialContext == nil {
		t.Fatal("connection safeguards lost")
	}
	if Client(time.Second).Timeout != time.Second || StreamingClient().Timeout != 0 {
		t.Fatal("client timeout changed")
	}
	ordinary, generation := 0, 0
	transport := healthTransport{
		ordinary: roundTripFunc(func(*http.Request) (*http.Response, error) {
			ordinary++
			return &http.Response{Body: io.NopCloser(strings.NewReader("ordinary"))}, nil
		}),
		generation: roundTripFunc(func(*http.Request) (*http.Response, error) {
			generation++
			return &http.Response{Body: io.NopCloser(strings.NewReader("generation"))}, nil
		}),
	}
	r, _ := http.NewRequest("GET", "http://example.test", nil)
	resp, err := transport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	ctx, stop := streaming.StartHealthMonitor(context.Background(), streaming.HealthOptions{})
	defer stop()
	resp, err = transport.RoundTrip(r.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ordinary != 1 || generation != 1 {
		t.Fatal("request-local selection")
	}
}

type closeTrackingTransport struct {
	closed int
}

func (t *closeTrackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func (t *closeTrackingTransport) CloseIdleConnections() { t.closed++ }

func TestHealthTransportCloseIdleConnections(t *testing.T) {
	ordinary, generation := &closeTrackingTransport{}, &closeTrackingTransport{}
	client := &http.Client{Transport: healthTransport{ordinary: ordinary, generation: generation}}
	client.CloseIdleConnections()
	if ordinary.closed != 1 || generation.closed != 1 {
		t.Fatalf("idle pools not closed: ordinary=%d generation=%d", ordinary.closed, generation.closed)
	}
}

func TestHealthBaseTransportsAndTLSReuse(t *testing.T) {
	ordinary, generation := BaseTransports()
	if ordinary != sharedTransport || generation != generationTransport || ordinary == generation {
		t.Fatal("accessor must expose both shared pools")
	}
	if Client(time.Second).Transport != Transport() || StreamingClient().Transport != Transport() {
		t.Fatal("clients must reuse the selector and connection pools")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.RemoteAddr)
	}))
	defer server.Close()
	// Configure isolated pools, retaining verified TLS and all connection policy.
	ordinary = ordinary.Clone()
	generation = generation.Clone()
	trusted := server.Client().Transport.(*http.Transport).TLSClientConfig
	ordinary.TLSClientConfig = trusted.Clone()
	generation.TLSClientConfig = trusted.Clone()
	client := &http.Client{Transport: healthTransport{ordinary: ordinary, generation: generation}}
	defer client.CloseIdleConnections()
	ctx, stop := streaming.StartHealthMonitor(context.Background(), streaming.HealthOptions{})
	defer stop()
	addresses := make(map[bool]string)
	for _, monitored := range []bool{false, true, false, true} {
		requestContext := context.Background()
		if monitored {
			requestContext = ctx
		}
		req, _ := http.NewRequestWithContext(requestContext, http.MethodGet, server.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if previous := addresses[monitored]; previous != "" && previous != string(body) {
			t.Fatalf("pool not reused: monitored=%v", monitored)
		}
		addresses[monitored] = string(body)
	}
	if addresses[false] == addresses[true] {
		t.Fatal("request modes must use separate connection pools")
	}
	if ordinary.TLSClientConfig.InsecureSkipVerify || generation.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS verification disabled")
	}
}

func TestActivityBodySixHoursActualReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := streaming.StartHealthMonitor(context.Background(), streaming.HealthOptions{IdleTimeout: 2 * time.Minute})
		defer stop()
		body := &activityBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", 360))), ctx: ctx}
		for i := 0; i < 360; i++ {
			time.Sleep(time.Minute)
			if n, err := body.Read(make([]byte, 1)); n != 1 || err != nil {
				t.Fatal(n, err)
			}
			if ctx.Err() != nil {
				t.Fatal(context.Cause(ctx))
			}
		}
		time.Sleep(3 * time.Minute)
		_, _ = body.Read(make([]byte, 1)) // EOF is not new activity.
		var stalled *streaming.GenerationStalledError
		if !errors.As(stop(), &stalled) {
			t.Fatal("silence not detected")
		}
	})
}

func TestHealthTransportPreHeaderSilence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx, stop := streaming.StartHealthMonitor(parent, streaming.HealthOptions{IdleTimeout: 30 * time.Millisecond, CheckInterval: 5 * time.Millisecond})
	defer stop()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, nil)
	resp, err := StreamingClient().Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected cancellation")
	}
	var stalled *streaming.GenerationStalledError
	if !errors.As(stop(), &stalled) {
		t.Fatalf("stall cause: %v", context.Cause(ctx))
	}
}
