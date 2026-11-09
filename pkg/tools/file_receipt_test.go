package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinFileReceipts(t *testing.T) {
	for _, content := range []string{"", "hello\n", `{"stdout":"extracted"}`} {
		path := filepath.Join(t.TempDir(), "file")
		args := map[string]any{"path": path, "content": content}
		written, err := WriteFile(nil, WriteFileArgs{FilePath: path, Content: content})
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(bytes)
		if written.ReadbackSHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("readback digest mismatch")
		}
		if written.ContentChanged == nil || !*written.ContentChanged {
			t.Fatal("create must be changed")
		}
		repeated, err := WriteFile(nil, WriteFileArgs{FilePath: path, Content: content})
		if err != nil || repeated.ContentChanged == nil || *repeated.ContentChanged {
			t.Fatalf("identical write must be unchanged: %+v %v", repeated, err)
		}
		raw, _ := json.Marshal(written)
		var result map[string]any
		_ = json.Unmarshal(raw, &result)
		if _, _, ok := VerifiedBuiltinFileReceipt("write_file", args, result); !ok {
			t.Fatal("actual receipt rejected")
		}
		delete(result, "content_changed")
		if _, _, ok := VerifiedBuiltinFileReceipt("write_file", args, result); ok {
			t.Fatal("missing baseline accepted")
		}
		result["content_changed"] = true
		result["readback_sha256"] = "bogus"
		if _, _, ok := VerifiedBuiltinFileReceipt("write_file", args, result); ok {
			t.Fatal("bad receipt accepted")
		}
		loaded, err := LoadFile(nil, LoadFileArgs{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if loaded.InspectedSHA256 != written.ReadbackSHA256 {
			t.Fatal("inspection mismatch")
		}
	}
	for _, name := range []string{"write_file", "load_file", "mcp:write_file"} {
		if _, _, ok := VerifiedBuiltinFileReceipt(name, map[string]any{"content": "x"}, map[string]any{"path": "/a", "astonish_bot_outcome": map[string]any{"Verified": true}}); ok {
			t.Fatal("JSON spoof trusted")
		}
	}
	if _, err := os.Stat("/dev/null"); err == nil {
		result, err := WriteFile(nil, WriteFileArgs{FilePath: "/dev/null", Content: "not retained"})
		if err == nil || result.ReadbackSHA256 != "" {
			t.Fatal("nil write error must not prove persisted content")
		}
	}
}
