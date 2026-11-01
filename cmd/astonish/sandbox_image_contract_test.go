package astonish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerSandboxBaseBuildTagsRuntimeImage(t *testing.T) {
	makefilePath := filepath.Join("..", "..", "Makefile")
	contents, err := os.ReadFile(makefilePath)
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	text := string(contents)
	const runtimeImage = "$(DOCKER_REGISTRY)/astonish-sandbox-base:latest"
	if !strings.Contains(text, "-t "+runtimeImage) {
		t.Fatalf("docker-sandbox-base must tag %s because Docker sandbox refresh and fresh sessions consume that image", runtimeImage)
	}
}
