package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"iter"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// CacheDiagnosticsHook receives one diagnostic after each model call.
type CacheDiagnosticsHook func(CacheDiagnostic)

// CacheDiagnostic describes the exact provider-neutral input and observed result
// of one model call. It contains hashes and counts, never model-visible content.
type CacheDiagnostic struct {
	Call                 int
	Stream               bool
	InputHash            string
	Elements             []ModelInputElement
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
	PromptTokens    int32
	CachedTokens    int32
	CandidateTokens int32
	ThoughtTokens   int32
	ToolUseTokens   int32
	TotalTokens     int32
}

type cacheDiagnosticsTracker struct {
	mu       sync.Mutex
	previous []ModelInputElement
	call     int
	hook     CacheDiagnosticsHook
}

type diagnosticLLM struct {
	model.LLM
	tracker *cacheDiagnosticsTracker
}

func newDiagnosticLLM(llm model.LLM, hook CacheDiagnosticsHook) model.LLM {
	if llm == nil {
		return nil
	}
	return &diagnosticLLM{
		LLM: llm,
		tracker: &cacheDiagnosticsTracker{
			hook: hook,
		},
	}
}

func (l *diagnosticLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		started := time.Now()
		elements, inputHash := canonicalModelInput(req)
		call, stableElements, stableBytes, divergence := l.tracker.begin(elements)
		diagnostic := CacheDiagnostic{
			Call:                 call,
			Stream:               stream,
			InputHash:            inputHash,
			Elements:             elements,
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
			if diagnostic.ResponseCount == 0 {
				diagnostic.TimeToFirstResponse = time.Since(started)
			}
			diagnostic.ResponseCount++
			if response != nil && response.UsageMetadata != nil {
				diagnostic.Usage.merge(diagnosticUsage(response.UsageMetadata))
			}
			if err != nil {
				diagnostic.Error = err.Error()
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
		PromptTokens:    usage.PromptTokenCount,
		CachedTokens:    usage.CachedContentTokenCount,
		CandidateTokens: usage.CandidatesTokenCount,
		ThoughtTokens:   usage.ThoughtsTokenCount,
		ToolUseTokens:   usage.ToolUsePromptTokenCount,
		TotalTokens:     usage.TotalTokenCount,
	}
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
