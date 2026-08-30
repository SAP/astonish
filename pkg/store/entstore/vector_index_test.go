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

func TestSortMemoryResultsBreaksScoreTiesByID(t *testing.T) {
	results := []store.MemorySearchResult{{ID: "b", Score: 0.5}, {ID: "a", Score: 0.5}}
	sortMemoryResults(results)
	if results[0].ID != "a" || results[1].ID != "b" {
		t.Fatalf("memory tie order = %#v, want a then b", results)
	}
}
