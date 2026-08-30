package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"iter"
	"testing"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestCanonicalModelInputPreservesOrderAndUsesFullHashes(t *testing.T) {
	request := diagnosticRequest("first", "second")
	elements, requestHash := canonicalModelInput(request)
	if len(requestHash) != sha256.Size*2 {
		t.Fatalf("request hash length = %d, want %d", len(requestHash), sha256.Size*2)
	}
	for _, element := range elements {
		if len(element.Hash) != sha256.Size*2 {
			t.Fatalf("%s hash length = %d, want %d", element.Path, len(element.Hash), sha256.Size*2)
		}
	}

	same := diagnosticRequest("first", "second")
	same.Contents[0].Parts[0].FunctionCall.Args = map[string]any{"a": 1, "b": 2}
	request.Contents[0].Parts[0].FunctionCall.Args = map[string]any{"b": 2, "a": 1}
	_, sameHash := canonicalModelInput(same)
	if sameHash != requestHash {
		t.Fatalf("equivalent JSON maps produced different hashes: %s != %s", requestHash, sameHash)
	}

	reordered := diagnosticRequest("second", "first")
	_, reorderedHash := canonicalModelInput(reordered)
	if reorderedHash == requestHash {
		t.Fatal("ordered model input hash did not change when contents were reordered")
	}
}

func TestStableModelInputPrefixReportsFirstDivergence(t *testing.T) {
	first, _ := canonicalModelInput(diagnosticRequest("first", "second"))
	second, _ := canonicalModelInput(diagnosticRequest("first", "changed"))

	stableElements, stableBytes := stableModelInputPrefix(first, second)
	if stableElements != len(first)-1 {
		t.Fatalf("stable elements = %d, want %d", stableElements, len(first)-1)
	}
	wantBytes := 0
	for _, element := range first[:stableElements] {
		wantBytes += element.Bytes
	}
	if stableBytes != wantBytes {
		t.Fatalf("stable bytes = %d, want %d", stableBytes, wantBytes)
	}

	tracker := &cacheDiagnosticsTracker{}
	_, _, _, firstDivergence := tracker.begin(first)
	if firstDivergence != "" {
		t.Fatalf("first call divergence = %q, want empty", firstDivergence)
	}
	_, gotElements, gotBytes, firstDivergence := tracker.begin(second)
	if gotElements != stableElements || gotBytes != stableBytes || firstDivergence != second[stableElements].Path {
		t.Fatalf("comparison = (%d, %d, %q), want (%d, %d, %q)", gotElements, gotBytes, firstDivergence, stableElements, stableBytes, second[stableElements].Path)
	}
}

func TestDiagnosticLLMCapturesTimingsUsageAndPreservesRequest(t *testing.T) {
	request := diagnosticRequest("hello")
	fake := &diagnosticTestLLM{request: request}
	var diagnostics []CacheDiagnostic
	llm := newDiagnosticLLM(fake, func(diagnostic CacheDiagnostic) {
		diagnostics = append(diagnostics, diagnostic)
	})

	var responses []*model.LLMResponse
	for response, err := range llm.GenerateContent(context.Background(), request, true) {
		if err != nil {
			t.Fatalf("GenerateContent() error = %v", err)
		}
		responses = append(responses, response)
	}
	if fake.gotRequest != request || !fake.gotStream {
		t.Fatal("diagnostic wrapper changed request identity or stream mode")
	}
	if len(responses) != 2 || len(diagnostics) != 1 {
		t.Fatalf("responses = %d, diagnostics = %d; want 2, 1", len(responses), len(diagnostics))
	}
	diagnostic := diagnostics[0]
	if diagnostic.Call != 1 || diagnostic.ResponseCount != 2 || diagnostic.InputHash == "" {
		t.Fatalf("diagnostic counters = %+v", diagnostic)
	}
	if diagnostic.TimeToFirstResponse <= 0 || diagnostic.Duration < diagnostic.TimeToFirstResponse {
		t.Fatalf("invalid timings: first=%s duration=%s", diagnostic.TimeToFirstResponse, diagnostic.Duration)
	}
	wantUsage := (CacheDiagnosticUsage{PromptTokens: 100, CachedTokens: 75, CandidateTokens: 20, TotalTokens: 120})
	if diagnostic.Usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", diagnostic.Usage, wantUsage)
	}
}

func TestDiagnosticLLMIsRequestScopedAndCapturesErrors(t *testing.T) {
	request := diagnosticRequest("hello")
	boom := errors.New("boom")
	var first, second CacheDiagnostic
	firstLLM := newDiagnosticLLM(&diagnosticTestLLM{err: boom}, func(d CacheDiagnostic) { first = d })
	secondLLM := newDiagnosticLLM(&diagnosticTestLLM{}, func(d CacheDiagnostic) { second = d })

	for range firstLLM.GenerateContent(context.Background(), request, false) {
	}
	for range secondLLM.GenerateContent(context.Background(), request, false) {
	}
	if first.Call != 1 || second.Call != 1 {
		t.Fatalf("request-scoped calls = %d, %d; want 1, 1", first.Call, second.Call)
	}
	if first.Error != boom.Error() {
		t.Fatalf("error = %q, want %q", first.Error, boom)
	}
}

func diagnosticRequest(contents ...string) *model.LLMRequest {
	request := &model.LLMRequest{
		Model: "test-model",
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("system", "system"),
			Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name: "lookup",
				ParametersJsonSchema: map[string]any{
					"type": "object",
				},
			}}}},
		},
	}
	for _, content := range contents {
		request.Contents = append(request.Contents, &genai.Content{
			Role: "user",
			Parts: []*genai.Part{{
				Text: content,
				FunctionCall: &genai.FunctionCall{
					Name: "lookup",
					Args: map[string]any{"a": 1, "b": 2},
				},
			}},
		})
	}
	return request
}

type diagnosticTestLLM struct {
	request    *model.LLMRequest
	gotRequest *model.LLMRequest
	gotStream  bool
	err        error
}

func (l *diagnosticTestLLM) Name() string { return "diagnostic-test" }

func (l *diagnosticTestLLM) GenerateContent(_ context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		l.gotRequest = request
		l.gotStream = stream
		if l.err != nil {
			yield(nil, l.err)
			return
		}
		time.Sleep(time.Millisecond)
		if !yield(&model.LLMResponse{Partial: true}, nil) {
			return
		}
		yield(&model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        100,
			CachedContentTokenCount: 75,
			CandidatesTokenCount:    20,
			TotalTokenCount:         120,
		}}, nil)
	}
}
