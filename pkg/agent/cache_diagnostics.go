package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"iter"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// CacheDiagnosticsHook receives one diagnostic after each model call.
type CacheDiagnosticsHook func(CacheDiagnostic)

// CacheDiagnostic describes the canonical ADK input and observed result of one model call.
type CacheDiagnostic struct {
	InvocationID         string
	Kind                 string
	Stage                string
	Status               string
	Call                 int
	Stream               bool
	Provider             string
	Model                string
	CaptureLevel         string
	InputHash            string
	Elements             []ModelInputElement
	Payload              json.RawMessage
	PayloadOriginalBytes int
	PayloadCapturedBytes int
	PayloadTruncated     bool
	BinaryElisions       int
	StablePrefixElements int
	StablePrefixBytes    int
	FirstDivergence      string
	StartedAt            time.Time
	TimeToFirstResponse  time.Duration
	Duration             time.Duration
	ResponseCount        int
	Usage                CacheDiagnosticUsage
	Error                string
}

// ModelInputElement is one ordered component of a canonical model request.
type ModelInputElement struct {
	Path  string
	Hash  string
	Bytes int
}

// CacheDiagnosticUsage is provider-neutral token usage reported by the model.
type CacheDiagnosticUsage struct {
	Reported        bool
	CacheReported   bool
	PromptTokens    int32
	CachedTokens    int32
	CandidateTokens int32
	ThoughtTokens   int32
	ToolUseTokens   int32
	TotalTokens     int32
}

type cacheDiagnosticsTracker struct {
	mu           sync.Mutex
	previous     []ModelInputElement
	call         int
	hook         CacheDiagnosticsHook
	invocationID string
	redactor     *credentials.Redactor
}

type diagnosticLLM struct {
	model.LLM
	tracker *cacheDiagnosticsTracker
}

type lifecycleDiagnosticRecorder struct {
	ctx          context.Context
	recorder     store.CacheDiagnosticRecorder
	invocationID string
	redactor     *credentials.Redactor
	mu           sync.Mutex
	diagnostics  []store.CacheDiagnostic
	wake         chan struct{}
	done         chan struct{}
	closed       bool
}

func lifecycleRecorder(ctx context.Context, invocationID string) *lifecycleDiagnosticRecorder {
	if !store.DebugEnabledFromContext(ctx) {
		return nil
	}
	recorder := store.CacheDiagnosticRecorderFromContext(ctx)
	if recorder == nil {
		return nil
	}
	r := &lifecycleDiagnosticRecorder{
		ctx:          ctx,
		recorder:     recorder,
		invocationID: invocationID,
		redactor:     credentials.RedactorFromContext(ctx),
		wake:         make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
	go r.persist()
	return r
}

func (r *lifecycleDiagnosticRecorder) begin(stage string) func(error) {
	if r == nil {
		return func(error) {}
	}
	started := time.Now()
	return func(stageErr error) {
		diagnostic := store.CacheDiagnostic{
			InvocationID: r.invocationID,
			Kind:         "preparation",
			Stage:        stage,
			Status:       "succeeded",
			StartedAt:    started,
			Duration:     time.Since(started),
			CreatedAt:    started,
		}
		if stageErr != nil {
			diagnostic.Status = "failed"
			diagnostic.Error = sanitizedDiagnosticError(stageErr.Error(), r.redactor)
		}
		r.enqueue(diagnostic)
	}
}

func (r *lifecycleDiagnosticRecorder) enqueue(diagnostic store.CacheDiagnostic) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.diagnostics = append(r.diagnostics, diagnostic)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *lifecycleDiagnosticRecorder) persist() {
	defer close(r.done)
	for range r.wake {
		for {
			r.mu.Lock()
			if len(r.diagnostics) == 0 {
				closed := r.closed
				r.mu.Unlock()
				if closed {
					return
				}
				break
			}
			diagnostic := r.diagnostics[0]
			r.diagnostics = r.diagnostics[1:]
			r.mu.Unlock()
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), time.Second)
			err := r.recorder(persistCtx, diagnostic)
			cancel()
			if err != nil {
				slog.Warn("persist cache diagnostic", "invocation", r.invocationID, "stage", diagnostic.Stage, "error", err)
			}
		}
	}
}

func (r *lifecycleDiagnosticRecorder) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	select {
	case <-r.done:
	case <-time.After(1100 * time.Millisecond):
		slog.Warn("timed out flushing cache diagnostics", "invocation", r.invocationID)
	}
}

func newDiagnosticLLM(llm model.LLM, hook CacheDiagnosticsHook, invocationID string, redactor *credentials.Redactor) model.LLM {
	if llm == nil || hook == nil {
		return llm
	}
	return &diagnosticLLM{
		LLM: llm,
		tracker: &cacheDiagnosticsTracker{
			hook:         hook,
			invocationID: invocationID,
			redactor:     redactor,
		},
	}
}

func (l *diagnosticLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		started := time.Now()
		elements, inputHash := canonicalModelInput(req)
		payload, originalBytes, truncated, binaryElisions := sanitizedModelPayload(req, l.tracker.redactor)
		call, stableElements, stableBytes, divergence := l.tracker.begin(elements)
		diagnostic := CacheDiagnostic{
			InvocationID:         l.tracker.invocationID,
			Kind:                 "provider",
			Stage:                "provider_dispatch",
			Status:               "succeeded",
			Call:                 call,
			Stream:               stream,
			Provider:             l.LLM.Name(),
			Model:                req.Model,
			CaptureLevel:         "canonical-adk",
			InputHash:            inputHash,
			Elements:             elements,
			Payload:              payload,
			PayloadOriginalBytes: originalBytes,
			PayloadCapturedBytes: len(payload),
			PayloadTruncated:     truncated,
			BinaryElisions:       binaryElisions,
			StablePrefixElements: stableElements,
			StablePrefixBytes:    stableBytes,
			FirstDivergence:      divergence,
			StartedAt:            started,
		}

		defer func() {
			diagnostic.Duration = time.Since(started)
			l.tracker.emit(diagnostic)
		}()

		for response, err := range l.LLM.GenerateContent(ctx, req, stream) {
			if response != nil {
				if diagnostic.ResponseCount == 0 {
					diagnostic.TimeToFirstResponse = time.Since(started)
				}
				diagnostic.ResponseCount++
			}
			if response != nil && response.UsageMetadata != nil {
				diagnostic.Usage.merge(diagnosticUsage(response.UsageMetadata))
			}
			if err != nil {
				diagnostic.Status = "failed"
				diagnostic.Error = sanitizedDiagnosticError(err.Error(), l.tracker.redactor)
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

func (t *cacheDiagnosticsTracker) begin(elements []ModelInputElement) (int, int, int, string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.call++
	stableElements, stableBytes := stableModelInputPrefix(t.previous, elements)
	divergence := ""
	if len(t.previous) > 0 && (stableElements < len(t.previous) || stableElements < len(elements)) {
		switch {
		case stableElements < len(elements):
			divergence = elements[stableElements].Path
		default:
			divergence = "end"
		}
	}
	t.previous = append([]ModelInputElement(nil), elements...)
	return t.call, stableElements, stableBytes, divergence
}

func (t *cacheDiagnosticsTracker) emit(diagnostic CacheDiagnostic) {
	if t.hook != nil {
		t.hook(diagnostic)
		return
	}
	slog.Debug("model cache diagnostic",
		"component", "chat-cache",
		"call", diagnostic.Call,
		"input_hash", diagnostic.InputHash,
		"elements", len(diagnostic.Elements),
		"stable_prefix_elements", diagnostic.StablePrefixElements,
		"stable_prefix_bytes", diagnostic.StablePrefixBytes,
		"first_divergence", diagnostic.FirstDivergence,
		"time_to_first_response", diagnostic.TimeToFirstResponse,
		"duration", diagnostic.Duration,
		"responses", diagnostic.ResponseCount,
		"prompt_tokens", diagnostic.Usage.PromptTokens,
		"cached_tokens", diagnostic.Usage.CachedTokens,
		"error", diagnostic.Error)
}

func canonicalModelInput(req *model.LLMRequest) ([]ModelInputElement, string) {
	if req == nil {
		return nil, hashBytes(nil)
	}

	elements := make([]ModelInputElement, 0, 2+len(req.Contents))
	appendElement := func(path string, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			data = nil
		}
		elements = append(elements, ModelInputElement{Path: path, Hash: hashBytes(data), Bytes: len(data)})
	}

	appendElement("model", req.Model)
	if req.Config != nil {
		if req.Config.SystemInstruction != nil {
			appendElement("system", req.Config.SystemInstruction)
		}
		for i, packed := range req.Config.Tools {
			appendElement(indexedPath("tool", i), packed)
		}
		config := *req.Config
		config.SystemInstruction = nil
		config.Tools = nil
		appendElement("config", &config)
	}
	for i, content := range req.Contents {
		appendElement(indexedPath("content", i), content)
	}

	hasher := sha256.New()
	for _, element := range elements {
		writeHashFrame(hasher, element.Path)
		writeHashFrame(hasher, element.Hash)
	}
	return elements, hex.EncodeToString(hasher.Sum(nil))
}

func indexedPath(prefix string, index int) string {
	return prefix + "[" + strconv.Itoa(index) + "]"
}

func writeHashFrame(hasher interface{ Write([]byte) (int, error) }, value string) {
	_, _ = hasher.Write([]byte(strconv.Itoa(len(value))))
	_, _ = hasher.Write([]byte{':'})
	_, _ = hasher.Write([]byte(value))
}

func stableModelInputPrefix(previous, current []ModelInputElement) (elements, bytes int) {
	limit := min(len(previous), len(current))
	for elements < limit {
		if previous[elements].Path != current[elements].Path || previous[elements].Hash != current[elements].Hash {
			break
		}
		bytes += current[elements].Bytes
		elements++
	}
	return elements, bytes
}

func (u *CacheDiagnosticUsage) merge(next CacheDiagnosticUsage) {
	u.Reported = u.Reported || next.Reported
	u.CacheReported = u.CacheReported || next.CacheReported
	if next.PromptTokens != 0 {
		u.PromptTokens = next.PromptTokens
	}
	if next.CachedTokens != 0 {
		u.CachedTokens = next.CachedTokens
	}
	if next.CandidateTokens != 0 {
		u.CandidateTokens = next.CandidateTokens
	}
	if next.ThoughtTokens != 0 {
		u.ThoughtTokens = next.ThoughtTokens
	}
	if next.ToolUseTokens != 0 {
		u.ToolUseTokens = next.ToolUseTokens
	}
	if next.TotalTokens != 0 {
		u.TotalTokens = next.TotalTokens
	}
}

func diagnosticUsage(usage *genai.GenerateContentResponseUsageMetadata) CacheDiagnosticUsage {
	if usage == nil {
		return CacheDiagnosticUsage{}
	}
	return CacheDiagnosticUsage{
		Reported:        true,
		CacheReported:   usage.CachedContentTokenCount > 0,
		PromptTokens:    usage.PromptTokenCount,
		CachedTokens:    usage.CachedContentTokenCount,
		CandidateTokens: usage.CandidatesTokenCount,
		ThoughtTokens:   usage.ThoughtsTokenCount,
		ToolUseTokens:   usage.ToolUsePromptTokenCount,
		TotalTokens:     usage.TotalTokenCount,
	}
}

const (
	maxDiagnosticPayloadBytes = 128 * 1024
	maxDiagnosticErrorBytes   = 4 * 1024
)

func sanitizedDiagnosticError(message string, redactor *credentials.Redactor) string {
	if redactor != nil {
		message = redactor.Redact(message)
	}
	if len(message) <= maxDiagnosticErrorBytes {
		return message
	}
	return message[:maxDiagnosticErrorBytes] + "… [truncated]"
}

func sanitizedModelPayload(req *model.LLMRequest, redactor *credentials.Redactor) (json.RawMessage, int, bool, int) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, 0, false, 0
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, len(raw), false, 0
	}
	elisions := 0
	value = sanitizeDiagnosticValue(value, "", redactor, &elisions)
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, len(raw), false, elisions
	}
	originalBytes := len(payload)
	if len(payload) <= maxDiagnosticPayloadBytes {
		return payload, originalBytes, false, elisions
	}
	manifest, _ := json.Marshal(map[string]any{
		"$truncated":    true,
		"originalBytes": originalBytes,
		"sha256":        hashBytes(payload),
	})
	return manifest, originalBytes, true, elisions
}

func sanitizeDiagnosticValue(value any, key string, redactor *credentials.Redactor, elisions *int) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for name := range typed {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(typed))
		for _, name := range keys {
			if sensitiveDiagnosticKey(name) {
				out[name] = "[REDACTED]"
				continue
			}
			out[name] = sanitizeDiagnosticValue(typed[name], name, redactor, elisions)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = sanitizeDiagnosticValue(typed[i], key, redactor, elisions)
		}
		return out
	case string:
		if shouldElideDiagnosticData(key, typed) {
			*elisions = *elisions + 1
			return map[string]any{"$elided": "binary", "encodedBytes": len(typed), "sha256": hashBytes([]byte(typed))}
		}
		if redactor != nil {
			return redactor.Redact(typed)
		}
		return typed
	default:
		return value
	}
}

func sensitiveDiagnosticKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
	for _, sensitive := range []string{"authorization", "apikey", "accesstoken", "refreshtoken", "idtoken", "clientsecret", "password", "secret", "cookie", "signature"} {
		if strings.Contains(normalized, sensitive) {
			return true
		}
	}
	return false
}

func shouldElideDiagnosticData(key, value string) bool {
	lowerKey := strings.ToLower(key)
	if strings.HasPrefix(value, "data:") && strings.Contains(value[:min(len(value), 128)], ";base64,") {
		return true
	}
	if (lowerKey == "data" || strings.Contains(lowerKey, "inline")) && len(value) >= 1024 {
		_, err := base64.StdEncoding.DecodeString(value)
		return err == nil
	}
	return false
}

func cacheDiagnosticForStore(d CacheDiagnostic) store.CacheDiagnostic {
	elements := make([]store.ModelInputElement, len(d.Elements))
	for i, element := range d.Elements {
		elements[i] = store.ModelInputElement{Path: element.Path, Hash: element.Hash, Bytes: element.Bytes}
	}
	return store.CacheDiagnostic{
		InvocationID:         d.InvocationID,
		Kind:                 d.Kind,
		Stage:                d.Stage,
		Status:               d.Status,
		Call:                 d.Call,
		Stream:               d.Stream,
		Provider:             d.Provider,
		Model:                d.Model,
		CaptureLevel:         d.CaptureLevel,
		InputHash:            d.InputHash,
		Elements:             elements,
		Payload:              d.Payload,
		PayloadOriginalBytes: d.PayloadOriginalBytes,
		PayloadCapturedBytes: d.PayloadCapturedBytes,
		PayloadTruncated:     d.PayloadTruncated,
		BinaryElisions:       d.BinaryElisions,
		StablePrefixElements: d.StablePrefixElements,
		StablePrefixBytes:    d.StablePrefixBytes,
		FirstDivergence:      d.FirstDivergence,
		StartedAt:            d.StartedAt,
		TimeToFirstResponse:  d.TimeToFirstResponse,
		Duration:             d.Duration,
		ResponseCount:        d.ResponseCount,
		Usage: store.CacheDiagnosticUsage{
			Reported:        d.Usage.Reported,
			CacheReported:   d.Usage.CacheReported,
			PromptTokens:    d.Usage.PromptTokens,
			CachedTokens:    d.Usage.CachedTokens,
			CandidateTokens: d.Usage.CandidateTokens,
			ThoughtTokens:   d.Usage.ThoughtTokens,
			ToolUseTokens:   d.Usage.ToolUseTokens,
			TotalTokens:     d.Usage.TotalTokens,
		},
		Error:     d.Error,
		CreatedAt: d.StartedAt,
	}
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
