package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// projectBotHomeHistory is a read-only model projection, never a transcript
// rewrite. The caller has already excluded the entire active run and bounded
// history at admission, so approval/question resumes keep their exact evidence.
func projectBotHomeHistory(prior []*session.Event) []*session.Event {
	type callKey struct{ invocation, branch, id, name string }
	calls := make(map[callKey]bool)
	projected := append([]*session.Event(nil), prior...)
	for i, e := range prior {
		if e == nil || e.Content == nil || e.Partial || e.ErrorCode != "" || e.ErrorMessage != "" {
			continue
		}
		for j, part := range e.Content.Parts {
			if part == nil {
				continue
			}
			if fc := part.FunctionCall; fc != nil && fc.ID != "" {
				calls[callKey{e.InvocationID, e.Branch, fc.ID, fc.Name}] = len(e.LongRunningToolIDs) == 0
			}
			fr := part.FunctionResponse
			if fr == nil || fr.ID == "" || !calls[callKey{e.InvocationID, e.Branch, fr.ID, fr.Name}] || fr.WillContinue != nil || fr.Scheduling != "" || len(fr.Parts) != 0 {
				continue
			}
			response := projectBotHomeReadResponse(fr)
			if response == nil {
				continue
			}
			// Copy every object we modify. Keep all other parts, ADK metadata, call
			// arguments, source IDs, and receipt/observation fields exactly as stored.
			if projected[i] == e {
				eventCopy, contentCopy := *e, *e.Content
				contentCopy.Parts = append([]*genai.Part(nil), e.Content.Parts...)
				eventCopy.Content = &contentCopy
				projected[i] = &eventCopy
			}
			partCopy, responseCopy := *part, *fr
			responseCopy.Response = response
			responseCopy.Response["_home_history_excerpt"] = fmt.Sprintf("Historical read excerpt; full result retained in Bot Home event %s (call %s). Omitted content is not evidence of absence; retrieve the source again if needed.", e.ID, fr.ID)
			partCopy.FunctionResponse = &responseCopy
			projected[i].Content.Parts[j] = &partCopy
		}
	}
	return projected
}

// Only the known successful WebFetchResult body is obsolete bulk data. Do not
// guess at arbitrary tools' output/result strings: they can be errors, pending
// approvals, mutation receipts, instructions or resumable execution state.
// This reuses Chat's truncateResult excerpt contract rather than forcing whole
// history compaction (which would summarize away authored instructions).
func projectBotHomeReadResponse(fr *genai.FunctionResponse) map[string]any {
	if fr.Name != "web_fetch" || fr.Response == nil || fr.Response["error"] != nil || fr.Response["_home_history_excerpt"] != nil {
		return nil
	}
	// ADK functiontool wraps typed results under output; persisted/legacy events
	// may already contain the WebFetchResult fields at the top level.
	root := fr.Response
	body := root
	wrapped := false
	if output, ok := root["output"]; ok {
		wrapped = true
		var ok bool
		body, ok = output.(map[string]any)
		if !ok {
			raw, err := json.Marshal(output)
			if err != nil || json.Unmarshal(raw, &body) != nil {
				return nil
			}
		}
	}
	if body["error"] != nil {
		return nil
	}
	// An explicit successful HTTP status is required; absent/unknown/error
	// results stay verbatim, including their bodies and diagnostic evidence.
	status, err := json.Marshal(body["status"])
	if err != nil || string(status) != "200" {
		return nil
	}
	content, ok := body["content"].(string)
	if !ok {
		return nil
	}
	excerpt := truncateResult(content)
	if excerpt == content {
		return nil
	}
	// The shared display helper cuts bytes. Repair only this new model view at
	// a UTF-8 boundary without changing ordinary Chat's display behavior.
	excerpt = strings.ToValidUTF8(excerpt, "")
	result := maps.Clone(root)
	if wrapped {
		body = maps.Clone(body)
		body["content"] = excerpt
		result["output"] = body
	} else {
		result["content"] = excerpt
	}
	return result
}
