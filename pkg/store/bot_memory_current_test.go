package store

import "testing"

func TestIsCurrentBotMemory(t *testing.T) {
	for _, tc := range []struct {
		marker  string
		current bool
	}{{"", true}, {"correction:old", true}, {"routine-operation:forget", false}, {"routine-operation:correct", false}, {"memory-review:replace", false}, {"control-proposal:forget", false}, {"expired", false}} {
		row := MemorySearchResult{Attribution: &MemoryAttribution{SupersededBy: tc.marker}}
		if got := IsCurrentBotMemory(row); got != tc.current {
			t.Errorf("%q: %v", tc.marker, got)
		}
	}
	if !IsCurrentBotMemory(MemorySearchResult{}) {
		t.Fatal("legacy memory lost")
	}
}
