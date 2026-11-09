package main

import "testing"

func TestRetainedArtifactContract(t *testing.T) {
	if retainedArtifactPath != "/root/chat-first-proof.txt" {
		t.Fatal("retained artifact path changed")
	}
	if retainedArtifactBytes != "CHAT_FIRST_REAL_TOOL_PROOF_20261106\n" {
		t.Fatal("exact approval payload changed")
	}
}
