package themes

import "testing"

func TestLookup(t *testing.T) {
	if _, err := Lookup("light-corporate"); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup("missing"); err == nil {
		t.Fatal("expected missing theme error")
	}
}
