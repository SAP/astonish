package common

import (
	"fmt"
	"maps"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// ParallelToolCallsOption is the OpenAI-compatible request extension carried by
// ADK's HTTPOptions.ExtraBody. It is not a provider-wide default or an approval.
const ParallelToolCallsOption = "parallel_tool_calls"

// WithParallelToolCalls copies the request and the modified config path so a
// checkpoint invocation cannot change ordinary Chat using the same provider.
func WithParallelToolCalls(req *model.LLMRequest, enabled bool) *model.LLMRequest {
	out := model.LLMRequest{}
	if req != nil {
		out = *req
	}
	cfg := genai.GenerateContentConfig{}
	if out.Config != nil {
		cfg = *out.Config
	}
	httpOptions := genai.HTTPOptions{}
	if cfg.HTTPOptions != nil {
		httpOptions = *cfg.HTTPOptions
	}
	httpOptions.ExtraBody = maps.Clone(httpOptions.ExtraBody)
	if httpOptions.ExtraBody == nil {
		httpOptions.ExtraBody = make(map[string]any)
	}
	httpOptions.ExtraBody[ParallelToolCallsOption] = enabled
	cfg.HTTPOptions = &httpOptions
	out.Config = &cfg
	return &out
}

// ParallelToolCalls reads the supported boolean extension. Absent means retain
// the provider default; malformed explicit values fail rather than silently
// enabling parallel generation. Other ExtraBody fields are not forwarded by this
// contract, and arbitrary provider request-body overrides are not enabled.
func ParallelToolCalls(req *model.LLMRequest) (*bool, error) {
	if req == nil || req.Config == nil || req.Config.HTTPOptions == nil {
		return nil, nil
	}
	raw, present := req.Config.HTTPOptions.ExtraBody[ParallelToolCallsOption]
	if !present {
		return nil, nil
	}
	enabled, ok := raw.(bool)
	if !ok {
		return nil, fmt.Errorf("%s request option must be a boolean", ParallelToolCallsOption)
	}
	return &enabled, nil
}

// WithoutParallelToolCalls removes this OpenAI-specific extension before native
// GenAI dispatch, where ExtraBody would otherwise send an unsupported field.
// Native providers still rely on the checkpoint's fail-closed response guard.
func WithoutParallelToolCalls(req *model.LLMRequest) *model.LLMRequest {
	if req == nil || req.Config == nil || req.Config.HTTPOptions == nil {
		return req
	}
	if _, present := req.Config.HTTPOptions.ExtraBody[ParallelToolCallsOption]; !present {
		return req
	}
	out := WithParallelToolCalls(req, false)
	delete(out.Config.HTTPOptions.ExtraBody, ParallelToolCallsOption)
	return out
}
