package entstore

import (
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestSQLiteFTSQueryEscapesConversationText(t *testing.T) {
	got := sqliteFTSQuery(`Use search_tools("memory") and foo:bar -baz OR "quoted"`)
	want := `"Use" OR "search_tools" OR "memory" OR "and" OR "foo" OR "bar" OR "baz" OR "OR" OR "quoted"`
	if got != want {
		t.Fatalf("sqliteFTSQuery() = %q, want %q", got, want)
	}
}

func TestVectorIndexSearchBreaksScoreTiesByID(t *testing.T) {
	index := newVectorIndex()
	for _, id := range []string{"c", "a", "b"} {
		index.add(id, []float32{1, 0})
	}

	got := index.search([]float32{1, 0}, 2, 0)
	if want := []scoredResult{{ID: "a", Score: 1}, {ID: "b", Score: 1}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("search results = %#v, want %#v", got, want)
	}
}

func TestRRFFuseBreaksScoreTiesByID(t *testing.T) {
	got := rrfFuse(
		[]scoredResult{{ID: "b"}, {ID: "a"}},
		[]scoredResult{{ID: "a"}, {ID: "b"}},
		60,
		2,
	)
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("fused tie order = %#v, want a then b", got)
	}
}

func TestRRFFuseNormalizesScores(t *testing.T) {
	// "a" appears at rank 0 in both lists → highest fused score.
	// "b" appears at rank 1 in both lists → lower.
	// "c" appears only in vector at rank 2 → lowest.
	got := rrfFuse(
		[]scoredResult{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		[]scoredResult{{ID: "a"}, {ID: "b"}},
		60,
		10,
	)
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d: %#v", len(got), got)
	}

	// Top result must be normalized to 1.0.
	if got[0].ID != "a" || got[0].Score != 1.0 {
		t.Fatalf("top result: got %v (score %.4f), want a (score 1.0)", got[0].ID, got[0].Score)
	}

	// All scores must be in (0, 1].
	for _, r := range got {
		if r.Score <= 0 || r.Score > 1.0 {
			t.Fatalf("score out of range (0,1]: id=%s score=%.6f", r.ID, r.Score)
		}
	}

	// Order must be a > b > c.
	if got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("unexpected order: %#v", got)
	}
	if got[1].Score >= got[0].Score || got[2].Score >= got[1].Score {
		t.Fatalf("scores not strictly descending: %#v", got)
	}
}

func TestSortMemoryResultsBreaksScoreTiesByID(t *testing.T) {
	results := []store.MemorySearchResult{{ID: "b", Score: 0.5}, {ID: "a", Score: 0.5}}
	sortMemoryResults(results)
	if results[0].ID != "a" || results[1].ID != "b" {
		t.Fatalf("memory tie order = %#v, want a then b", results)
	}
}
