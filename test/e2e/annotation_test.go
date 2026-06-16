//go:build e2e
// +build e2e

// This e2e test requires a running OCI registry. Start one with either:
//
//	docker run -d -p 5555:5000 --name ds-porter-registry registry:2
//
// or with podman:
//
//	podman run -d -p 5555:5000 --name ds-porter-registry registry:2
//
// Then run with: go test -tags=e2e -v ./test/e2e/...
package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/delivery-station/porter/pkg/release"
)

const (
	testRegistry   = "localhost:5555"
	testRepository = "e2e-test/porter-annotations"
)

func skipIfRegistryUnreachable(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", testRegistry, 2*time.Second)
	if err != nil {
		t.Skipf("Registry %s is not available, skipping e2e test", testRegistry)
	}
	_ = conn.Close()
}

// TestAnnotationEndToEnd verifies that annotations at both artifact and entry
// levels are correctly pushed to an OCI registry and can be retrieved.
func TestAnnotationEndToEnd(t *testing.T) {
	skipIfRegistryUnreachable(t)

	tmpDir := t.TempDir()

	// Create test binaries
	linuxBin := filepath.Join(tmpDir, "linux-amd64")
	darwinBin := filepath.Join(tmpDir, "darwin-arm64")
	if err := os.WriteFile(linuxBin, []byte("linux-amd64-binary-content"), 0755); err != nil {
		t.Fatalf("failed to create linux binary: %v", err)
	}
	if err := os.WriteFile(darwinBin, []byte("darwin-arm64-binary-content"), 0755); err != nil {
		t.Fatalf("failed to create darwin binary: %v", err)
	}

	// Create manifest with both top-level and per-entry annotations
	manifestPath := filepath.Join(tmpDir, "ds.manifest.yaml")
	manifestYAML := `
artifact-type: application/vnd.delivery-station.artifact.index.v1+json
annotations:
  name: e2e-test-app
  version: "1.0.0"
  org.opencontainers.image.source: https://github.com/delivery-station/ds-porter
manifests:
  - path: linux-amd64
    platform: linux/amd64
    mediaType: application/vnd.delivery-station.artifact.v1+binary
    annotations:
      org.opencontainers.image.title: e2e-linux-amd64
      org.opencontainers.image.description: Linux AMD64 test binary
      custom.e2e.platform: linux
  - path: darwin-arm64
    platform: darwin/arm64
    mediaType: application/vnd.delivery-station.artifact.v1+binary
    annotations:
      org.opencontainers.image.title: e2e-darwin-arm64
      org.opencontainers.image.description: Darwin ARM64 test binary
      custom.e2e.platform: darwin
`
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	ref := fmt.Sprintf("%s/%s:%s", testRegistry, testRepository, "e2e-annotation-test")

	// Push using the release package
	pusher, err := release.NewPusher(release.ReleaseConfig{
		Reference:    ref,
		ManifestPath: manifestPath,
		Insecure:     true, // HTTP registry
	})
	if err != nil {
		t.Fatalf("failed to create pusher: %v", err)
	}

	ctx := context.Background()
	if err := pusher.Push(ctx, nil); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	t.Cleanup(func() {
		_ = deleteFromRegistry(ref)
	})

	// Validate using go-containerregistry
	t.Run("ValidateAnnotationsViaRegistry", func(t *testing.T) {
		refParsed, err := name.ParseReference(ref, name.Insecure)
		if err != nil {
			t.Fatalf("failed to parse reference: %v", err)
		}

		// Fetch the index
		idx, err := remote.Index(refParsed)
		if err != nil {
			t.Fatalf("failed to fetch index: %v", err)
		}

		manifest, err := idx.IndexManifest()
		if err != nil {
			t.Fatalf("failed to get index manifest: %v", err)
		}

		if manifest.Annotations == nil {
			t.Fatal("index has no annotations")
		}

		expectedTopLevel := map[string]string{
			"name":    "e2e-test-app",
			"version": "1.0.0",
		}
		for k, want := range expectedTopLevel {
			if got := manifest.Annotations[k]; got != want {
				t.Errorf("index annotation %s = %q, want %q", k, got, want)
			}
		}

		if len(manifest.Manifests) != 2 {
			t.Fatalf("expected 2 platform descriptors, got %d", len(manifest.Manifests))
		}

		// Each platform descriptor should have per-entry annotations
		platformAnnotations := map[string]map[string]string{
			"e2e-linux-amd64": {
				"org.opencontainers.image.description": "Linux AMD64 test binary",
				"custom.e2e.platform":                  "linux",
			},
			"e2e-darwin-arm64": {
				"org.opencontainers.image.description": "Darwin ARM64 test binary",
				"custom.e2e.platform":                  "darwin",
			},
		}

		for _, platformDesc := range manifest.Manifests {
			title, ok := platformDesc.Annotations["org.opencontainers.image.title"]
			if !ok {
				t.Error("layer descriptor missing org.opencontainers.image.title annotation")
				continue
			}

			expected, exists := platformAnnotations[title]
			if !exists {
				t.Errorf("unexpected platform title: %s", title)
				continue
			}

			for k, want := range expected {
				if got := platformDesc.Annotations[k]; got != want {
					t.Errorf("layer %q annotation %s = %q, want %q", title, k, got, want)
				}
			}
		}
	})
}

// TestArtifactLevelAnnotationsOnly verifies that top-level artifact annotations
// work correctly even when entries have no annotations.
func TestArtifactLevelAnnotationsOnly(t *testing.T) {
	skipIfRegistryUnreachable(t)

	tmpDir := t.TempDir()

	binPath := filepath.Join(tmpDir, "app")
	if err := os.WriteFile(binPath, []byte("single-binary"), 0755); err != nil {
		t.Fatalf("failed to create binary: %v", err)
	}

	manifestPath := filepath.Join(tmpDir, "ds.manifest.yaml")
	manifestYAML := `
artifact-type: application/vnd.delivery-station.artifact.index.v1+json
annotations:
  name: artifact-only-app
  version: "2.0.0"
manifests:
  - path: app
    platform: linux/amd64
    mediaType: application/vnd.delivery-station.artifact.v1+binary
`
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	ref := fmt.Sprintf("%s/%s:%s", testRegistry, testRepository, "e2e-artifact-only")

	pusher, err := release.NewPusher(release.ReleaseConfig{
		Reference:    ref,
		ManifestPath: manifestPath,
		Insecure:     true,
	})
	if err != nil {
		t.Fatalf("failed to create pusher: %v", err)
	}

	ctx := context.Background()
	if err := pusher.Push(ctx, nil); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	t.Cleanup(func() {
		_ = deleteFromRegistry(ref)
	})

	refParsed, err := name.ParseReference(ref, name.Insecure)
	if err != nil {
		t.Fatalf("failed to parse reference: %v", err)
	}

	idx, err := remote.Index(refParsed)
	if err != nil {
		t.Fatalf("failed to fetch index: %v", err)
	}

	manifest, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("failed to get index manifest: %v", err)
	}
	if manifest.Annotations == nil {
		t.Fatal("index has no annotations")
	}
	if manifest.Annotations["name"] != "artifact-only-app" {
		t.Errorf("index annotation name = %q, want %q", manifest.Annotations["name"], "artifact-only-app")
	}
	if manifest.Annotations["version"] != "2.0.0" {
		t.Errorf("index annotation version = %q, want %q", manifest.Annotations["version"], "2.0.0")
	}
}

// TestMixedAnnotations verifies that entries with and without annotations
// coexist correctly in the same index.
func TestMixedAnnotations(t *testing.T) {
	skipIfRegistryUnreachable(t)

	tmpDir := t.TempDir()

	bin1 := filepath.Join(tmpDir, "app-with-annotations")
	bin2 := filepath.Join(tmpDir, "app-no-annotations")
	if err := os.WriteFile(bin1, []byte("with-annotations"), 0755); err != nil {
		t.Fatalf("failed to create bin1: %v", err)
	}
	if err := os.WriteFile(bin2, []byte("no-annotations"), 0755); err != nil {
		t.Fatalf("failed to create bin2: %v", err)
	}

	manifestPath := filepath.Join(tmpDir, "ds.manifest.yaml")
	manifestYAML := `
artifact-type: application/vnd.delivery-station.artifact.index.v1+json
annotations:
  name: mixed-annotations-app
manifests:
  - path: app-with-annotations
    platform: linux/amd64
    mediaType: application/vnd.delivery-station.artifact.v1+binary
    annotations:
      custom.has-annotations: "true"
  - path: app-no-annotations
    platform: linux/arm64
    mediaType: application/vnd.delivery-station.artifact.v1+binary
`
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	ref := fmt.Sprintf("%s/%s:%s", testRegistry, testRepository, "e2e-mixed")

	pusher, err := release.NewPusher(release.ReleaseConfig{
		Reference:    ref,
		ManifestPath: manifestPath,
		Insecure:     true,
	})
	if err != nil {
		t.Fatalf("failed to create pusher: %v", err)
	}

	ctx := context.Background()
	if err := pusher.Push(ctx, nil); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	t.Cleanup(func() {
		_ = deleteFromRegistry(ref)
	})

	refParsed, err := name.ParseReference(ref, name.Insecure)
	if err != nil {
		t.Fatalf("failed to parse reference: %v", err)
	}

	idx, err := remote.Index(refParsed)
	if err != nil {
		t.Fatalf("failed to fetch index: %v", err)
	}

	manifest, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("failed to get index manifest: %v", err)
	}

	if len(manifest.Manifests) != 2 {
		t.Fatalf("expected 2 layers, got %d", len(manifest.Manifests))
	}

	hasAnnotations := false
	noAnnotations := false
	for _, layerDesc := range manifest.Manifests {
		if layerDesc.Annotations != nil && len(layerDesc.Annotations) > 0 {
			hasAnnotations = true
			if layerDesc.Annotations["custom.has-annotations"] != "true" {
				t.Errorf("expected custom.has-annotations=true, got %v", layerDesc.Annotations)
			}
		} else {
			noAnnotations = true
		}
	}

	if !hasAnnotations {
		t.Error("expected at least one layer with annotations")
	}
	if !noAnnotations {
		t.Error("expected at least one layer without annotations")
	}
}

// deleteFromRegistry deletes a tagged reference from the registry.
func deleteFromRegistry(ref string) error {
	refParsed, err := name.ParseReference(ref, name.Insecure)
	if err != nil {
		return err
	}
	return remote.Delete(refParsed)
}
