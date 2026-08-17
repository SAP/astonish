package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	nurl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/adk/tool"
)

const (
	httpReqDefaultTimeout  = 30 * time.Second
	httpReqMaxTimeout      = 120 * time.Second
	httpReqMaxRedirects    = 5
	httpReqDefaultMaxBytes = 2 * 1024 * 1024 // 2MB
	httpReqUserAgent       = "Astonish/1.0"
)

// httpReqSkipSSRF is a test hook to bypass SSRF checks for httptest servers.
var httpReqSkipSSRF bool

var httpReqAllowedMethods = map[string]bool{
	"GET":     true,
	"POST":    true,
	"PUT":     true,
	"PATCH":   true,
	"DELETE":  true,
	"HEAD":    true,
	"OPTIONS": true,
}

// HttpMultipartPart is one field in a multipart/form-data body.
type HttpMultipartPart struct {
	Name        string `json:"name" jsonschema:"Form field name"`
	Value       string `json:"value,omitempty" jsonschema:"Text value for this field (mutually exclusive with file_path)"`
	FilePath    string `json:"file_path,omitempty" jsonschema:"Path to a file to upload (sandbox-visible). Mutually exclusive with value."`
	Filename    string `json:"filename,omitempty" jsonschema:"Optional filename override for file parts (defaults to basename of file_path)"`
	ContentType string `json:"content_type,omitempty" jsonschema:"Optional Content-Type for this part"`
}

type HttpRequestArgs struct {
	URL         string              `json:"url" jsonschema:"The URL to send the request to (http or https)"`
	Method      string              `json:"method,omitempty" jsonschema:"HTTP method: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS. Default: GET"`
	Headers     map[string]string   `json:"headers,omitempty" jsonschema:"Additional HTTP headers as key-value pairs"`
	Body        string              `json:"body,omitempty" jsonschema:"Request body string (for POST, PUT, PATCH). Mutually exclusive with body_path and multipart."`
	BodyPath    string              `json:"body_path,omitempty" jsonschema:"Path to a file whose contents are sent as the raw request body (e.g. MP4 upload). Mutually exclusive with body and multipart."`
	Multipart   []HttpMultipartPart `json:"multipart,omitempty" jsonschema:"multipart/form-data parts. Mutually exclusive with body and body_path. Prefer this over curl for Synthesia-style asset uploads."`
	ContentType string              `json:"content_type,omitempty" jsonschema:"Override Content-Type for body_path uploads (default: mime from extension or application/octet-stream)"`
	Credential  string              `json:"credential,omitempty" jsonschema:"Name of a stored credential for authentication. The credential's auth header is added automatically."`
	Timeout     int                 `json:"timeout,omitempty" jsonschema:"Request timeout in seconds. Default 30, max 120."`
	MaxBytes    int                 `json:"max_bytes,omitempty" jsonschema:"Maximum response body size in bytes. Default 2MB."`
}

type HttpRequestResult struct {
	StatusCode  int               `json:"status_code"`
	Status      string            `json:"status"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	ContentType string            `json:"content_type"`
	DurationMs  int64             `json:"duration_ms"`
	Truncated   bool              `json:"truncated,omitempty"`
}

// HttpRequest makes an HTTP request with optional credential injection.
func HttpRequest(ctx tool.Context, args HttpRequestArgs) (HttpRequestResult, error) {
	var c context.Context = context.Background()
	if ctx != nil {
		c = ctx
	}
	return httpRequest(c, args)
}

// httpRequest is the shared implementation used by ADK tools and astonish node.
func httpRequest(ctx context.Context, args HttpRequestArgs) (HttpRequestResult, error) {
	if args.URL == "" {
		return HttpRequestResult{}, fmt.Errorf("url is required")
	}

	parsedURL, err := nurl.ParseRequestURI(args.URL)
	if err != nil {
		return HttpRequestResult{}, fmt.Errorf("invalid URL: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return HttpRequestResult{}, fmt.Errorf("only http and https URLs are supported, got %q", parsedURL.Scheme)
	}

	if !httpReqSkipSSRF && !httpReqAllowPrivateNetworks {
		if err := checkSSRF(parsedURL.Hostname()); err != nil {
			return HttpRequestResult{}, err
		}
	}

	bodyModes := 0
	if args.Body != "" {
		bodyModes++
	}
	if args.BodyPath != "" {
		bodyModes++
	}
	if len(args.Multipart) > 0 {
		bodyModes++
	}
	if bodyModes > 1 {
		return HttpRequestResult{}, fmt.Errorf("body, body_path, and multipart are mutually exclusive — use only one")
	}

	method := strings.ToUpper(args.Method)
	if method == "" {
		method = "GET"
	}
	if !httpReqAllowedMethods[method] {
		return HttpRequestResult{}, fmt.Errorf("unsupported HTTP method %q: allowed methods are GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS", args.Method)
	}

	timeout := httpReqDefaultTimeout
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout) * time.Second
		if timeout > httpReqMaxTimeout {
			timeout = httpReqMaxTimeout
		}
	}

	maxBytes := int64(httpReqDefaultMaxBytes)
	if args.MaxBytes > 0 {
		maxBytes = int64(args.MaxBytes)
	}

	bodyReader, contentType, cleanup, err := buildHTTPRequestBody(args)
	if err != nil {
		return HttpRequestResult{}, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	req, err := http.NewRequest(method, args.URL, bodyReader)
	if err != nil {
		return HttpRequestResult{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", httpReqUserAgent)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	if args.Body != "" && req.Header.Get("Content-Type") == "" {
		trimmed := strings.TrimSpace(args.Body)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			req.Header.Set("Content-Type", "application/json")
		}
	}

	for k, v := range args.Headers {
		req.Header.Set(k, v)
	}

	if args.Credential != "" {
		cs := getEffectiveCredStore(ctx)
		if cs == nil {
			return HttpRequestResult{}, fmt.Errorf("credential store is not available — cannot resolve credential %q", args.Credential)
		}
		headerKey, headerValue, err := cs.Resolve(ctx, args.Credential)
		if err != nil {
			return HttpRequestResult{}, fmt.Errorf("failed to resolve credential %q: %w", args.Credential, err)
		}
		req.Header.Set(headerKey, headerValue)
	}

	redirectCount := 0
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			redirectCount++
			if redirectCount > httpReqMaxRedirects {
				return fmt.Errorf("too many redirects (max %d)", httpReqMaxRedirects)
			}
			return nil
		},
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return HttpRequestResult{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	durationMs := time.Since(start).Milliseconds()

	limitedReader := io.LimitReader(resp.Body, maxBytes+1)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return HttpRequestResult{}, fmt.Errorf("failed to read response body: %w", err)
	}

	truncated := false
	if int64(len(bodyBytes)) > maxBytes {
		bodyBytes = bodyBytes[:maxBytes]
		truncated = true
	}

	bodyStr := string(bodyBytes)

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") || strings.Contains(ct, "+json") {
		if pretty, err := prettyJSON(bodyStr); err == nil {
			bodyStr = pretty
		}
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k, vals := range resp.Header {
		if len(vals) > 0 {
			respHeaders[k] = vals[0]
		}
	}

	return HttpRequestResult{
		StatusCode:  resp.StatusCode,
		Status:      resp.Status,
		Headers:     respHeaders,
		Body:        bodyStr,
		ContentType: ct,
		DurationMs:  durationMs,
		Truncated:   truncated,
	}, nil
}

func buildHTTPRequestBody(args HttpRequestArgs) (io.Reader, string, func(), error) {
	if args.Body != "" {
		return strings.NewReader(args.Body), "", nil, nil
	}

	if args.BodyPath != "" {
		f, err := os.Open(args.BodyPath) // #nosec G304 -- agent-supplied sandbox path for intentional upload
		if err != nil {
			return nil, "", nil, fmt.Errorf("body_path: %w", err)
		}
		ct := args.ContentType
		if ct == "" {
			ct = mime.TypeByExtension(filepath.Ext(args.BodyPath))
			if ct == "" {
				ct = "application/octet-stream"
			}
		}
		return f, ct, func() { _ = f.Close() }, nil
	}

	if len(args.Multipart) == 0 {
		return nil, "", nil, nil
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for i, part := range args.Multipart {
		if part.Name == "" {
			return nil, "", nil, fmt.Errorf("multipart[%d]: name is required", i)
		}
		if part.Value != "" && part.FilePath != "" {
			return nil, "", nil, fmt.Errorf("multipart[%d]: value and file_path are mutually exclusive", i)
		}
		if part.FilePath != "" {
			f, err := os.Open(part.FilePath) // #nosec G304 -- agent-supplied sandbox path
			if err != nil {
				return nil, "", nil, fmt.Errorf("multipart[%d] file_path: %w", i, err)
			}
			filename := part.Filename
			if filename == "" {
				filename = filepath.Base(part.FilePath)
			}
			var fw io.Writer
			if part.ContentType != "" {
				h := make(map[string][]string)
				h["Content-Disposition"] = []string{
					fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(part.Name), escapeQuotes(filename)),
				}
				h["Content-Type"] = []string{part.ContentType}
				fw, err = w.CreatePart(h)
			} else {
				fw, err = w.CreateFormFile(part.Name, filename)
			}
			if err != nil {
				_ = f.Close()
				return nil, "", nil, fmt.Errorf("multipart[%d]: %w", i, err)
			}
			_, copyErr := io.Copy(fw, f)
			_ = f.Close()
			if copyErr != nil {
				return nil, "", nil, fmt.Errorf("multipart[%d]: copy: %w", i, copyErr)
			}
			continue
		}
		if err := w.WriteField(part.Name, part.Value); err != nil {
			return nil, "", nil, fmt.Errorf("multipart[%d]: %w", i, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", nil, fmt.Errorf("multipart close: %w", err)
	}
	return &buf, w.FormDataContentType(), nil, nil
}

func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}
