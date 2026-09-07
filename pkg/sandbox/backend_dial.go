// backend_dial.go — exported helpers for dialing ports inside sandbox
// sessions via the Backend interface. These are used by pkg/api proxy
// handlers that need net.Conn or *http.Transport connections to
// container services without depending on a concrete backend (Docker,
// K8s, OpenShell).

package sandbox

import (
	"context"
	"net"
	"net/http"
)

// DialSessionPort creates a net.Conn to 127.0.0.1:port inside the session
// identified by sessionID, using the Backend's ExecStreaming to run
// `socat STDIO TCP:127.0.0.1:<port>` inside the container.
//
// This is the backend-agnostic equivalent of the legacy ContainerDialer.Dial.
// It works across all backend implementations (Docker, K8s,
// OpenShell) because it tunnels through the Backend exec API.
func DialSessionPort(ctx context.Context, backend Backend, sessionID string, port int) (net.Conn, error) {
	return dialBackendSessionPort(ctx, backend, sessionID, port)
}

// BackendHTTPTransport returns an *http.Transport that routes all
// connections through DialSessionPort for the given session and port.
// The transport's DialContext ignores the network/address parameters —
// the destination is fixed to sessionID:port.
//
// This is the backend-agnostic equivalent of the legacy ContainerDialer.HTTPTransport.
func BackendHTTPTransport(backend Backend, sessionID string, port int) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return DialSessionPort(ctx, backend, sessionID, port)
		},
		MaxIdleConnsPerHost: 2,
	}
}
