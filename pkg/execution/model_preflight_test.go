package execution

import (
	"context"
	"testing"
)

func TestModelPreflightEndContextIsolation(t *testing.T) {
	a, b := 0, 0
	parent := context.Background()
	first := WithModelPreflightEnd(parent, func() { a++ })
	second := WithModelPreflightEnd(parent, func() { b++ })
	EndModelPreflight(parent)
	EndModelPreflight(first)
	if a != 1 || b != 0 {
		t.Fatalf("cross-context cleanup a=%d b=%d", a, b)
	}
	EndModelPreflight(second)
	if a != 1 || b != 1 {
		t.Fatalf("missing cleanup a=%d b=%d", a, b)
	}
}
