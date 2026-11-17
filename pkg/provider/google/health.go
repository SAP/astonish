package google

import (
	"bufio"
	"context"
	"io"
	"mime"
	"net/http"

	"github.com/SAP/astonish/pkg/provider/streaming"
)

// nativeHealthTransport observes the SDK's real response reads, including SSE
// comments that never produce an ADK response. Keep the SDK's default transport
// policy (proxy, TLS, dialing and timeouts), rather than replacing it with a
// differently configured pool or mutating a shared transport. In particular,
// the native SDK has no default total or response-header generation deadline.
// Explicit request HTTPOptions timeouts and parent deadlines remain binding.
type nativeHealthTransport struct {
	base http.RoundTripper
}

func (t nativeHealthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || !streaming.HealthEnabled(req.Context()) {
		return resp, err
	}
	streaming.ReportUpstreamActivity(req.Context())
	if resp.Body != nil {
		resp.Body = &nativeActivityBody{ReadCloser: resp.Body, ctx: req.Context()}
		// go-genai v1.76 rejects legal SSE comment lines. Strip only comments
		// for monitored streams, after observing their actual network bytes.
		// No synthetic events or semantic progress are used as health evidence.
		if mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mediaType == "text/event-stream" {
			resp.Body = &nativeSSEBody{ReadCloser: resp.Body, reader: bufio.NewReader(resp.Body), lineStart: true}
		}
	}
	return resp, nil
}

func (t nativeHealthTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// nativeSSEBody removes comment lines with bounded buffering; even a comment
// longer than the reader buffer is discarded incrementally. All other bytes are
// returned unchanged, leaving JSON decoding and terminal semantics to the SDK.
type nativeSSEBody struct {
	io.ReadCloser
	reader    *bufio.Reader
	pending   []byte
	lineStart bool
	comment   bool
	err       error
}

func (b *nativeSSEBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(b.pending) == 0 {
		if b.err != nil {
			return 0, b.err
		}
		fragment, err := b.reader.ReadSlice('\n')
		if b.lineStart && len(fragment) > 0 {
			b.comment = fragment[0] == ':'
		}
		b.lineStart = err != bufio.ErrBufferFull
		if err != nil && err != bufio.ErrBufferFull {
			b.err = err
		}
		if !b.comment {
			b.pending = fragment
		}
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

type nativeActivityBody struct {
	io.ReadCloser
	ctx context.Context
}

func (b *nativeActivityBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		streaming.ReportUpstreamActivity(b.ctx)
	}
	return n, err
}
