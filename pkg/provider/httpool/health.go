package httpool

import (
	"context"
	"io"
	"net/http"

	"github.com/SAP/astonish/pkg/provider/streaming"
)

// Separate immutable connection pool: monitored generation is bounded by its
// factual-activity watchdog, not the legacy response-header deadline. Connection
// establishment safeguards are retained. Never mutate an in-use transport.
var generationTransport = func() *http.Transport {
	t := sharedTransport.Clone()
	t.ResponseHeaderTimeout = 0
	return t
}()

var pooledTransport http.RoundTripper = healthTransport{ordinary: sharedTransport, generation: generationTransport}

type healthTransport struct {
	ordinary   http.RoundTripper
	generation http.RoundTripper
}

// CloseIdleConnections preserves http.Client.CloseIdleConnections behavior for
// both pools. Auth wrappers may also forward this optional transport method.
func (t healthTransport) CloseIdleConnections() {
	for _, pool := range []http.RoundTripper{t.ordinary, t.generation} {
		if closer, ok := pool.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

func (t healthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !streaming.HealthEnabled(req.Context()) {
		return t.ordinary.RoundTrip(req)
	}
	resp, err := t.generation.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	streaming.ReportUpstreamActivity(req.Context())
	if resp.Body != nil {
		resp.Body = &activityBody{ReadCloser: resp.Body, ctx: req.Context()}
	}
	return resp, nil
}

type activityBody struct {
	io.ReadCloser
	ctx context.Context
}

func (b *activityBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		streaming.ReportUpstreamActivity(b.ctx)
	}
	return n, err
}
