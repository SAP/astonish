package components

import "testing"

func TestSchemaV1(t *testing.T) {
	s, ok := SchemaV1("ast-image")
	if !ok || len(s.Required) == 0 {
		t.Fatalf("unexpected schema: %#v", s)
	}
	if _, ok := SchemaV1("iframe"); ok {
		t.Fatal("iframe must not be registered")
	}
}
