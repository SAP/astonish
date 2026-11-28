// Package httpool provides a shared, pool-aware HTTP client for LLM providers.
// Using shared transports avoids connection churn under concurrent workloads.
package httpool

import (
	"net"
	"net/http"
	"time"
)

// sharedTransport retains the bounded response-header wait for unmonitored
// calls (including auth and discovery). The generation pool is cloned once.
var sharedTransport = &http.Transport{
	MaxIdleConns:          200,
	MaxIdleConnsPerHost:   20,
	MaxConnsPerHost:       0,
	IdleConnTimeout:       120 * time.Second,
	DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout:   15 * time.Second,
	ExpectContinueTimeout: time.Second,
	ResponseHeaderTimeout: 120 * time.Second,
	ForceAttemptHTTP2:     true,
}

// Client returns a client backed by shared pools. The supplied timeout remains
// a total request limit; use 0 when context controls streaming cancellation.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: pooledTransport, Timeout: timeout}
}

// StreamingClient has no total timeout. Only monitored requests bypass the
// ordinary response-header timeout; upstream activity is observed on reads.
func StreamingClient() *http.Client { return Client(0) }

// Transport returns a request-local pool selector for custom auth wrappers.
// Unmonitored requests retain all ordinary transport timeouts. The result is not
// a *http.Transport; use BaseTransports to inspect the underlying pools.
func Transport() http.RoundTripper { return pooledTransport }

// BaseTransports returns the ordinary and monitored-generation connection pools.
// Both are shared across clients. Treat them as immutable after use: changing
// fields while requests are in flight is unsafe. Callers configuring transport
// policy before use must configure both pools consistently (including TLS and
// dial policy), preserving the generation pool's zero ResponseHeaderTimeout.
// For isolated clients, clone rather than mutate these shared transports.
func BaseTransports() (ordinary, generation *http.Transport) {
	return sharedTransport, generationTransport
}
