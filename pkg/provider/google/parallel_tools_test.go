package google

import (
	"context"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/common"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type nativeSerialConfigFixture struct {
	t     *testing.T
	calls int
}

func (f *nativeSerialConfigFixture) Name() string { return "native-fixture" }
func (f *nativeSerialConfigFixture) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		f.calls++
		if _, present := req.Config.HTTPOptions.ExtraBody[common.ParallelToolCallsOption]; present {
			f.t.Error("OpenAI option leaked into native GenAI request")
		}
		if req.Config.HTTPOptions.ExtraBody["native_option"] != "retained" {
			f.t.Error("native option lost")
		}
		yield(&model.LLMResponse{}, nil)
	}
}

func TestNativeProviderStripsParallelToolCallsExtension(t *testing.T) {
	req := common.WithParallelToolCalls(&model.LLMRequest{Config: &genai.GenerateContentConfig{HTTPOptions: &genai.HTTPOptions{ExtraBody: map[string]any{"native_option": "retained"}}}}, false)
	fixture := &nativeSerialConfigFixture{t: t}
	p := &Provider{model: fixture}
	for _, err := range p.GenerateContent(t.Context(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fixture.calls != 1 {
		t.Fatal("native provider not called")
	}
	if got, _ := common.ParallelToolCalls(req); got == nil || *got {
		t.Fatal("native conversion mutated caller config")
	}
}
