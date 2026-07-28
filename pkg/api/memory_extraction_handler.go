package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// --------------------------------------------------------------------------
// Memory Extraction: LLM-based consolidation of session memories
// --------------------------------------------------------------------------

// extractionSystemPrompt instructs the LLM on how to consolidate session memories.
const extractionSystemPrompt = `You are a memory consolidation assistant. You receive a list of memories that were saved during a single session. Your job is to convert useful durable knowledge into efficient scenario-card material.

Rules:
1. Group related memories by repeatable task or scenario.
2. Keep the shortest successful path and durable conditions.
3. Treat temporary failures, outages, and trial/error as conditional cautions only.
4. Do not preserve raw scattered notes just because they exist.
5. If a memory cannot be turned into a useful repeatable scenario card, omit it; the system can learn it again later.
6. NEVER add information that wasn't in the original memories.
7. NEVER save secret values (passwords, tokens, API keys).

Output format: Call consolidate_memories with an array of consolidated entries. Each entry has a "category" (short scenario heading) and "content" (bullet-point successful path, conditions, and conditional cautions).`

// MemoryExtractionRequest is the request body for POST /api/memories/session/{id}/extract.
type MemoryExtractionRequest struct {
	// DryRun if true returns the preview without saving. Default is true.
	DryRun *bool `json:"dry_run,omitempty"`
}

// MemoryExtractionEntry is a single consolidated memory entry.
type MemoryExtractionEntry struct {
	Category string `json:"category"`
	Content  string `json:"content"`
}

// MemoryExtractionResponse is the response from the extraction endpoint.
type MemoryExtractionResponse struct {
	SessionID     string                  `json:"session_id"`
	OriginalCount int                     `json:"original_count"`
	Entries       []MemoryExtractionEntry `json:"entries"`
	Applied       bool                    `json:"applied"`
}

// MemoryExtractHandler consolidates session memories using LLM.
// POST /api/memories/session/{id}/extract
// By default returns a dry-run preview. Set dry_run=false to apply.
func MemoryExtractHandler(w http.ResponseWriter, r *http.Request) {
	pu := GetPlatformUser(r)
	if pu == nil {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	sessionID := mux.Vars(r)["id"]
	if sessionID == "" {
		respondError(w, http.StatusBadRequest, "session id required")
		return
	}

	var req MemoryExtractionRequest
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}

	svc := store.FromRequest(r)
	if svc == nil {
		respondError(w, http.StatusInternalServerError, "services not available")
		return
	}

	// Collect memories from both team and personal stores for this session
	var allMemories []store.MemorySearchResult

	teamStore, err := resolveMemoryStoreForScope(r, svc, pu, "team")
	if err == nil && teamStore != nil {
		results, err := teamStore.ListBySession(r.Context(), sessionID)
		if err == nil {
			allMemories = append(allMemories, results...)
		}
	}

	personalStore, err := resolveMemoryStoreForScope(r, svc, pu, "personal")
	if err == nil && personalStore != nil {
		results, err := personalStore.ListBySession(r.Context(), sessionID)
		if err == nil {
			allMemories = append(allMemories, results...)
		}
	}

	if len(allMemories) == 0 {
		respondJSON(w, http.StatusOK, MemoryExtractionResponse{
			SessionID:     sessionID,
			OriginalCount: 0,
			Entries:       []MemoryExtractionEntry{},
			Applied:       false,
		})
		return
	}

	// Get LLM from ChatManager
	cm := GetChatManager()
	if err := cm.ensureReady(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, "LLM not available: "+err.Error())
		return
	}
	llm := cm.components.LLM
	if llm == nil {
		respondError(w, http.StatusInternalServerError, "LLM not configured")
		return
	}

	// Build the user prompt with all session memories
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Here are %d memories saved during session %s:\n\n", len(allMemories), sessionID))
	for i, m := range allMemories {
		sb.WriteString(fmt.Sprintf("--- Memory %d ---\n", i+1))
		sb.WriteString(fmt.Sprintf("Category: %s\n", m.Category))
		sb.WriteString(fmt.Sprintf("Scope: %s\n", m.Scope))
		sb.WriteString(fmt.Sprintf("Content:\n%s\n\n", m.Snippet))
	}
	sb.WriteString("Please consolidate these memories into well-organized entries grouped by topic.")

	// Call LLM with consolidate_memories tool
	llmReq := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Parts: []*genai.Part{{Text: sb.String()}},
				Role:  "user",
			},
		},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{
				Parts: []*genai.Part{{Text: extractionSystemPrompt}},
			},
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        "consolidate_memories",
					Description: "Output the consolidated memory entries.",
					ParametersJsonSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"entries": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"category": map[string]any{
											"type":        "string",
											"description": "Short category heading for this group",
										},
										"content": map[string]any{
											"type":        "string",
											"description": "Consolidated content as bullet points",
										},
									},
									"required": []string{"category", "content"},
								},
								"description": "Array of consolidated memory entries",
							},
						},
						"required": []string{"entries"},
					},
				}},
			}},
		},
	}

	resp, err := callLLMForExtraction(r, llm, llmReq)
	if err != nil {
		slog.Error("memory extraction LLM call failed", "error", err, "session_id", sessionID)
		respondError(w, http.StatusInternalServerError, "LLM extraction failed: "+err.Error())
		return
	}

	// Parse the LLM response — look for consolidate_memories function call
	entries := parseExtractionResponse(resp)

	if len(entries) == 0 {
		// LLM didn't return structured output; return original memories as-is
		for _, m := range allMemories {
			entries = append(entries, MemoryExtractionEntry{
				Category: m.Category,
				Content:  m.Snippet,
			})
		}
	}

	applied := false
	if !dryRun && len(entries) > 0 {
		// Apply: delete old session memories and insert consolidated ones
		applied = applyExtraction(r, svc, pu, sessionID, allMemories, entries, teamStore, personalStore)
		if applied {
			invalidateMemoryHealthCache()
		}
	}

	respondJSON(w, http.StatusOK, MemoryExtractionResponse{
		SessionID:     sessionID,
		OriginalCount: len(allMemories),
		Entries:       entries,
		Applied:       applied,
	})
}

// callLLMForExtraction calls the LLM using GenerateContent and returns the last response.
func callLLMForExtraction(r *http.Request, llm model.LLM, req *model.LLMRequest) (*model.LLMResponse, error) {
	var lastResp *model.LLMResponse
	for resp, err := range llm.GenerateContent(r.Context(), req, false) {
		if err != nil {
			return nil, err
		}
		lastResp = resp
	}
	return lastResp, nil
}

// parseExtractionResponse extracts MemoryExtractionEntry items from the LLM response.
func parseExtractionResponse(resp *model.LLMResponse) []MemoryExtractionEntry {
	if resp == nil || resp.Content == nil {
		return nil
	}
	for _, part := range resp.Content.Parts {
		if part.FunctionCall != nil && part.FunctionCall.Name == "consolidate_memories" {
			args := part.FunctionCall.Args
			if args == nil {
				continue
			}
			entriesRaw, ok := args["entries"]
			if !ok {
				continue
			}
			// entriesRaw should be []any where each item is map[string]any
			entriesSlice, ok := entriesRaw.([]any)
			if !ok {
				continue
			}
			var entries []MemoryExtractionEntry
			for _, item := range entriesSlice {
				itemMap, ok := item.(map[string]any)
				if !ok {
					continue
				}
				cat, _ := itemMap["category"].(string)
				content, _ := itemMap["content"].(string)
				if cat != "" && content != "" {
					entries = append(entries, MemoryExtractionEntry{
						Category: cat,
						Content:  content,
					})
				}
			}
			return entries
		}
	}
	return nil
}

// applyExtraction deletes original session memories and writes scenario cards.
// Returns true if the operation succeeded.
func applyExtraction(r *http.Request, svc *store.Services, pu *PlatformUser, sessionID string,
	originals []store.MemorySearchResult, entries []MemoryExtractionEntry,
	teamStore, personalStore store.MemoryStore) bool {

	ctx := r.Context()

	// Determine target store — use team store if available (extracted memories are team-level)
	targetStore := teamStore
	targetScope := "team"
	if targetStore == nil {
		targetStore = personalStore
		targetScope = "personal"
	}
	if targetStore == nil {
		slog.Error("extraction: no target store available")
		return false
	}

	for _, entry := range entries {
		memEntry := store.MemoryEntry{
			Content:   entry.Content,
			Category:  entry.Category,
			SessionID: sessionID,
			CreatedBy: pu.ID,
		}
		card := memory.DraftScenarioCardFromMemoryEntry(targetScope, memEntry)
		if !memory.HasUsableScenarioRecipe(card) {
			continue
		}
		if _, err := memory.UpsertScenarioCard(ctx, targetStore, card); err != nil {
			slog.Warn("extraction: failed to save scenario card", "category", entry.Category, "error", err)
			return false
		}
	}

	for _, m := range originals {
		switch m.Scope {
		case "team":
			if teamStore != nil {
				if err := teamStore.Delete(ctx, m.ID); err != nil {
					slog.Warn("extraction: failed to delete original memory", "id", m.ID, "error", err)
				}
			}
		case "personal":
			if personalStore != nil {
				if err := personalStore.Delete(ctx, m.ID); err != nil {
					slog.Warn("extraction: failed to delete original memory", "id", m.ID, "error", err)
				}
			}
		}
	}

	return true
}
