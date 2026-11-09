package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// VerifiedBuiltinFileReceipt validates only receipts produced by the built-in
// file implementations. Call ONLY for the pinned built-in sandbox node response,
// never for MCP, model arguments, transcript JSON, or arbitrary tool results.
// This is not a decoder for bot.ToolOutcome: actor/time/source are server-bound.
func VerifiedBuiltinFileReceipt(name string, args, result map[string]any) (string, string, bool) {
	path, _ := result["path"].(string)
	if path == "" {
		return "", "", false
	}
	switch name {
	case "write_file":
		// A missing baseline cannot prove changed OR unchanged content.
		if _, ok := result["content_changed"].(bool); !ok {
			return "", "", false
		}
		content, ok := args["content"].(string)
		if !ok {
			return "", "", false
		}
		if extracted, ok := tryExtractStdout(content); ok {
			content = extracted
		}
		digest := sha256.Sum256([]byte(content))
		if result["readback_sha256"] != hex.EncodeToString(digest[:]) {
			return "", "", false
		}
		return path, fmt.Sprintf("File content read back and matched SHA-256 %x", digest), true
	case "load_file":
		digest, ok := result["inspected_sha256"].(string)
		if !ok {
			return "", "", false
		}
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return "", "", false
		}
		return path, "File bytes inspected for Files publication; SHA-256 " + digest, true
	}
	return "", "", false
}
