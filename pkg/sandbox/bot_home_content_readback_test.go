package sandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestBotHomeContentReadback(t *testing.T) {
	raw := []byte("stored\x00bytes\n")
	sum := sha256.Sum256(raw)
	for _, tc := range []struct {
		name  string
		body  []byte
		size  int64
		valid bool
	}{
		{"valid", raw, int64(len(raw)), true},
		{"short", raw[:2], int64(len(raw)), false},
		{"long", append(bytes.Clone(raw), 'x'), int64(len(raw)), false},
		{"forged", bytes.Repeat([]byte("!"), len(raw)), int64(len(raw)), false},
		{"bound", nil, BotHomeContentReadbackLimit + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadBotHomeContentReadback(bytes.NewReader(tc.body), tc.size, hex.EncodeToString(sum[:]))
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
			if tc.valid && !bytes.Equal(got, raw) {
				t.Fatal("changed bytes")
			}
		})
	}
}
