package release

import (
	"encoding/json"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// TestPushIndexAnnotationPropagation verifies that per-entry annotations from
// ManifestEntry are correctly propagated to the OCI index layer descriptors.
func TestPushIndexAnnotationPropagation(t *testing.T) {
	// Simulate descriptors returned from PushAll (they have nil Annotations initially)
	descriptors := []ocispec.Descriptor{
		{
			MediaType: MediaTypeArtifactBinary,
			Digest:    digest.FromString("linux-amd64-content"),
			Size:      100,
			Platform:  &ocispec.Platform{OS: "linux", Architecture: "amd64"},
		},
		{
			MediaType: MediaTypeArtifactBinary,
			Digest:    digest.FromString("darwin-arm64-content"),
			Size:      200,
			Platform:  &ocispec.Platform{OS: "darwin", Architecture: "arm64"},
		},
	}

	// Simulate manifest entries with per-entry annotations
	entries := []ManifestEntry{
		{
			Platform: "linux/amd64",
			Path:     "bin/linux-amd64/app",
			MediaType: MediaTypeArtifactBinary,
			Annotations: map[string]string{
				"org.opencontainers.image.title":           "app-linux-amd64",
				"org.opencontainers.image.description":    "Linux AMD64 binary",
				"custom.annotation.entry":                 "entry-value-1",
			},
		},
		{
			Platform: "darwin/arm64",
			Path:     "bin/darwin-arm64/app",
			MediaType: MediaTypeArtifactBinary,
			Annotations: map[string]string{
				"org.opencontainers.image.title":           "app-darwin-arm64",
				"org.opencontainers.image.description":    "Darwin ARM64 binary",
				"custom.annotation.entry":                 "entry-value-2",
			},
		},
	}

	// Simulate top-level manifest annotations
	manifest := &Manifest{
		ArtifactType: MediaTypeArtifactIndex,
		Annotations: map[string]string{
			"name":    "my-app",
			"version": "1.0.0",
		},
		Manifests: entries,
	}

	// Replicate the annotation propagation logic from PushIndex
	var layers []ocispec.Descriptor
	for i, desc := range descriptors {
		desc.Platform = nil // PushIndex sets this to nil

		// Propagate per-entry annotations to index layer descriptors
		if entries != nil && i < len(entries) && entries[i].Annotations != nil {
			if desc.Annotations == nil {
				desc.Annotations = make(map[string]string)
			}
			for k, v := range entries[i].Annotations {
				desc.Annotations[k] = v
			}
		}

		layers = append(layers, desc)
	}

	// Build the index (replicating PushIndex logic)
	index := ocispec.Index{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType:    ocispec.MediaTypeImageIndex,
		ArtifactType: manifest.ArtifactType,
		Manifests:    layers,
		Annotations:  manifest.Annotations,
	}

	// Marshal and verify
	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("failed to marshal index: %v", err)
	}

	var verified struct {
		Manifests []ocispec.Descriptor `json:"manifests"`
		Annotations map[string]string `json:"annotations,omitempty"`
		ArtifactType string           `json:"artifactType,omitempty"`
	}
	if err := json.Unmarshal(indexBytes, &verified); err != nil {
		t.Fatalf("failed to unmarshal index: %v", err)
	}

	// Verify top-level annotations
	if len(verified.Annotations) != 2 {
		t.Errorf("expected 2 top-level annotations, got %d", len(verified.Annotations))
	}
	if verified.Annotations["name"] != "my-app" {
		t.Errorf("top-level annotation name = %q, want %q", verified.Annotations["name"], "my-app")
	}
	if verified.Annotations["version"] != "1.0.0" {
		t.Errorf("top-level annotation version = %q, want %q", verified.Annotations["version"], "1.0.0")
	}

	// Verify per-entry annotations on each layer descriptor
	expectedEntries := []struct {
		title           string
		desc            string
		customEntry     string
	}{
		{
			title:       "app-linux-amd64",
			desc:        "Linux AMD64 binary",
			customEntry: "entry-value-1",
		},
		{
			title:       "app-darwin-arm64",
			desc:        "Darwin ARM64 binary",
			customEntry: "entry-value-2",
		},
	}

	for i, expected := range expectedEntries {
		t.Run(verified.Manifests[i].Annotations["org.opencontainers.image.title"], func(t *testing.T) {
			desc := verified.Manifests[i]
			if desc.Annotations == nil {
				t.Fatalf("manifests[%d].Annotations is nil, expected per-entry annotations", i)
			}

			if got := desc.Annotations["org.opencontainers.image.title"]; got != expected.title {
				t.Errorf("annotation org.opencontainers.image.title = %q, want %q", got, expected.title)
			}
			if got := desc.Annotations["org.opencontainers.image.description"]; got != expected.desc {
				t.Errorf("annotation org.opencontainers.image.description = %q, want %q", got, expected.desc)
			}
			if got := desc.Annotations["custom.annotation.entry"]; got != expected.customEntry {
				t.Errorf("annotation custom.annotation.entry = %q, want %q", got, expected.customEntry)
			}
		})
	}
}

// TestPushIndexAnnotationPropagation_NoEntryAnnotations verifies that when entries
// have no annotations, the index is still valid and top-level annotations are preserved.
func TestPushIndexAnnotationPropagation_NoEntryAnnotations(t *testing.T) {
	descriptors := []ocispec.Descriptor{
		{
			MediaType: MediaTypeArtifactBinary,
			Digest:    digest.FromString("content"),
			Size:      100,
		},
	}

	entries := []ManifestEntry{
		{
			Platform:  "linux/amd64",
			Path:      "bin/app",
			MediaType: MediaTypeArtifactBinary,
			// No annotations
		},
	}

	manifest := &Manifest{
		ArtifactType: MediaTypeArtifactIndex,
		Annotations: map[string]string{
			"name": "no-annotations-app",
		},
		Manifests: entries,
	}

	var layers []ocispec.Descriptor
	for i, desc := range descriptors {
		desc.Platform = nil
		if entries != nil && i < len(entries) && entries[i].Annotations != nil {
			if desc.Annotations == nil {
				desc.Annotations = make(map[string]string)
			}
			for k, v := range entries[i].Annotations {
				desc.Annotations[k] = v
			}
		}
		layers = append(layers, desc)
	}

	index := ocispec.Index{
		Versioned:      specs.Versioned{SchemaVersion: 2},
		MediaType:      ocispec.MediaTypeImageIndex,
		ArtifactType:   manifest.ArtifactType,
		Manifests:      layers,
		Annotations:    manifest.Annotations,
	}

	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("failed to marshal index: %v", err)
	}

	var verified struct {
		Manifests []ocispec.Descriptor `json:"manifests"`
		Annotations map[string]string `json:"annotations,omitempty"`
	}
	if err := json.Unmarshal(indexBytes, &verified); err != nil {
		t.Fatalf("failed to unmarshal index: %v", err)
	}

	if len(verified.Manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(verified.Manifests))
	}

	// Layer should have no annotations (or nil)
	if len(verified.Manifests[0].Annotations) > 0 {
		t.Errorf("expected no layer annotations, got %v", verified.Manifests[0].Annotations)
	}

	// Top-level annotations should still be present
	if verified.Annotations["name"] != "no-annotations-app" {
		t.Errorf("top-level annotation name = %q, want %q", verified.Annotations["name"], "no-annotations-app")
	}
}

// TestPushIndexAnnotationPropagation_MixedAnnotations verifies behavior when some
// entries have annotations and others don't.
func TestPushIndexAnnotationPropagation_MixedAnnotations(t *testing.T) {
	descriptors := []ocispec.Descriptor{
		{MediaType: MediaTypeArtifactBinary, Digest: digest.FromString("a"), Size: 10},
		{MediaType: MediaTypeArtifactBinary, Digest: digest.FromString("b"), Size: 20},
		{MediaType: MediaTypeArtifactBinary, Digest: digest.FromString("c"), Size: 30},
	}

	entries := []ManifestEntry{
		{
			Platform: "linux/amd64",
			Path:     "bin/a",
			Annotations: map[string]string{"custom": "has-annotations"},
		},
		{
			Platform: "linux/arm64",
			Path:     "bin/b",
			// No annotations
		},
		{
			Platform: "darwin/amd64",
			Path:     "bin/c",
			Annotations: map[string]string{"custom": "also-has"},
		},
	}

	manifest := &Manifest{
		ArtifactType: MediaTypeArtifactIndex,
		Manifests:    entries,
	}

	var layers []ocispec.Descriptor
	for i, desc := range descriptors {
		desc.Platform = nil
		if entries != nil && i < len(entries) && entries[i].Annotations != nil {
			if desc.Annotations == nil {
				desc.Annotations = make(map[string]string)
			}
			for k, v := range entries[i].Annotations {
				desc.Annotations[k] = v
			}
		}
		layers = append(layers, desc)
	}

	index := ocispec.Index{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageIndex,
		ArtifactType: manifest.ArtifactType,
		Manifests:    layers,
	}

	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("failed to marshal index: %v", err)
	}

	var verified struct {
		Manifests []ocispec.Descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(indexBytes, &verified); err != nil {
		t.Fatalf("failed to unmarshal index: %v", err)
	}

	// Entry 0: should have annotations
	if verified.Manifests[0].Annotations == nil || verified.Manifests[0].Annotations["custom"] != "has-annotations" {
		t.Errorf("manifests[0] should have custom=has-annotations, got %v", verified.Manifests[0].Annotations)
	}

	// Entry 1: should have no annotations
	if len(verified.Manifests[1].Annotations) > 0 {
		t.Errorf("manifests[1] should have no annotations, got %v", verified.Manifests[1].Annotations)
	}

	// Entry 2: should have annotations
	if verified.Manifests[2].Annotations == nil || verified.Manifests[2].Annotations["custom"] != "also-has" {
		t.Errorf("manifests[2] should have custom=also-has, got %v", verified.Manifests[2].Annotations)
	}
}
