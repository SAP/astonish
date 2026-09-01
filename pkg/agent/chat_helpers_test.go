package agent

import (
	"testing"

	"google.golang.org/adk/session"
)

func TestBuildBM25Query(t *testing.T) {
	tests := []struct {
		name  string
		query string
		tail  string
		want  string
	}{
		{
			name:  "no_tail",
			query: "compute-3.qa-de-1.cloud.sap credentials",
			tail:  "",
			want:  "compute-3.qa-de-1.cloud.sap credentials",
		},
		{
			name:  "with_tail",
			query: "compute-3.qa-de-1.cloud.sap credentials",
			tail:  "previous model response",
			want:  "previous model response compute-3.qa-de-1.cloud.sap credentials",
		},
		{
			name:  "short_query",
			query: "hi",
			tail:  "",
			want:  "",
		},
		{
			name:  "exact_threshold",
			query: "abcde",
			tail:  "",
			want:  "abcde",
		},
		{
			name:  "below_threshold",
			query: "abcd",
			tail:  "some tail",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildBM25Query(tt.query, tt.tail)
			if got != tt.want {
				t.Errorf("buildBM25Query(%q, %q) = %q, want %q", tt.query, tt.tail, got, tt.want)
			}
		})
	}
}

func TestYieldKnowledgeTrackingEventIncludesQueryAndProvenance(t *testing.T) {
	var got *session.Event
	yieldKnowledgeTrackingEvent(func(ev *session.Event, err error) bool {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got = ev
		return true
	}, "proxmox console", "prior context proxmox console", "knowledge", "", []KnowledgeSearchResult{
		{
			ID:        "mem-1",
			Path:      "memory/proxmox",
			Score:     0.91,
			Category:  "proxmox",
			Scope:     "team",
			CreatedBy: "user-1",
			CreatedAt: "2026-07-27T10:00:00Z",
			SessionID: "session-1",
		},
	})

	if got == nil {
		t.Fatal("expected tracking event")
	}
	payload, ok := got.Actions.StateDelta["_knowledge_injection"].(map[string]any)
	if !ok {
		t.Fatalf("missing _knowledge_injection payload: %#v", got.Actions.StateDelta)
	}
	if payload["query"] != "proxmox console" {
		t.Fatalf("query = %v, want proxmox console", payload["query"])
	}
	if payload["bm25_query_len"] != len("prior context proxmox console") {
		t.Fatalf("bm25_query_len = %v", payload["bm25_query_len"])
	}
	if payload["result_count"] != 1 {
		t.Fatalf("result_count = %v, want 1", payload["result_count"])
	}

	results, ok := payload["results"].([]map[string]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %#v, want one result", payload["results"])
	}
	result := results[0]
	for key, want := range map[string]any{
		"id":         "mem-1",
		"scope":      "team",
		"created_by": "user-1",
		"created_at": "2026-07-27T10:00:00Z",
		"session_id": "session-1",
	} {
		if result[key] != want {
			t.Fatalf("result[%s] = %v, want %v", key, result[key], want)
		}
	}
}
