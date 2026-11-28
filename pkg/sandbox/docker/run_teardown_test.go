package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifySessionDestroyedPhysicalDockerResponse(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		gone         bool
	}{
		{"present", "echo container-id\n", false},
		{"absent", "echo 'Error: No such object: session' >&2\nexit 1\n", true},
		{"unavailable", "echo 'Cannot connect to Docker daemon' >&2\nexit 1\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "docker")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.script), 0700); err != nil {
				t.Fatal(err)
			}
			b := &DockerBackend{}
			b.cfg.ContainerRuntimePath = path
			err := b.VerifySessionDestroyed(context.Background(), "exact-run")
			if (err == nil) != tc.gone {
				t.Fatalf("gone=%v error=%v", tc.gone, err)
			}
		})
	}
}
