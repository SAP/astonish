package web

import (
	"bytes"
	"testing"
)

func TestGetSlidesRuntime(t *testing.T) {
	runtime := GetSlidesRuntime()
	if len(runtime) == 0 {
		t.Fatal("embedded slides runtime is empty")
	}
	if !bytes.Contains(runtime, []byte("ast-deck")) {
		t.Fatal("embedded slides runtime does not register ast-deck")
	}
}
