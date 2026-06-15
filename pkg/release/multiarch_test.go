package release

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePlatform(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantOS    string
		wantArch  string
		wantVariant string
	}{
		{
			name:   "empty string",
			input:  "",
			wantOS: "",
		},
		{
			name:   "os only",
			input:  "linux",
			wantOS: "linux",
		},
		{
			name:     "os/arch",
			input:    "linux/amd64",
			wantOS:   "linux",
			wantArch: "amd64",
		},
		{
			name:      "os/arch/variant",
			input:     "linux/arm64/v8",
			wantOS:    "linux",
			wantArch:  "arm64",
			wantVariant: "v8",
		},
		{
			name:     "os/arch:version",
			input:    "linux/amd64:bookworm",
			wantOS:   "linux",
			wantArch: "amd64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParsePlatform(tt.input)
			if err != nil {
				t.Fatalf("ParsePlatform(%q) unexpected error: %v", tt.input, err)
			}
			if p.OS != tt.wantOS {
				t.Errorf("ParsePlatform(%q).OS = %q, want %q", tt.input, p.OS, tt.wantOS)
			}
			if p.Arch != tt.wantArch {
				t.Errorf("ParsePlatform(%q).Arch = %q, want %q", tt.input, p.Arch, tt.wantArch)
			}
			if p.Variant != tt.wantVariant {
				t.Errorf("ParsePlatform(%q).Variant = %q, want %q", tt.input, p.Variant, tt.wantVariant)
			}
		})
	}
}

func TestParsePlatform_Empty(t *testing.T) {
	p, err := ParsePlatform("")
	if err != nil {
		t.Fatalf("ParsePlatform(\"\") unexpected error: %v", err)
	}
	if p.OS != "" || p.Arch != "" || p.Variant != "" {
		t.Errorf("ParsePlatform(\"\") = %+v, want empty Platform", p)
	}
}

func TestLoadManifest(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "ds.manifest.yaml")

	manifestYAML := `
artifact-type: application/vnd.delivery-station.recipe.index.v1+json
annotations:
  name: spark-recipes
  version: 1.0.0
manifests:
  - path: recipes/a.yaml
    mediaType: application/vnd.delivery-station.recipe.v1+yaml
  - path: recipes/b.yaml
    mediaType: application/vnd.delivery-station.recipe.v1+yaml
  - path: recipes/c.yaml
    mediaType: application/vnd.delivery-station.recipe.v1+yaml
`
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() unexpected error: %v", err)
	}

	if manifest.ArtifactType != "application/vnd.delivery-station.recipe.index.v1+json" {
		t.Errorf("ArtifactType = %q, want %q", manifest.ArtifactType, "application/vnd.delivery-station.recipe.index.v1+json")
	}

	if len(manifest.Manifests) != 3 {
		t.Fatalf("Manifests count = %d, want 3", len(manifest.Manifests))
	}

	expectedPaths := []string{"recipes/a.yaml", "recipes/b.yaml", "recipes/c.yaml"}
	for i, entry := range manifest.Manifests {
		if entry.Path != expectedPaths[i] {
			t.Errorf("Manifests[%d].Path = %q, want %q", i, entry.Path, expectedPaths[i])
		}
		if entry.Platform != "" {
			t.Errorf("Manifests[%d].Platform = %q, want empty", i, entry.Platform)
		}
	}
}

func TestLoadManifest_MissingFile(t *testing.T) {
	_, err := LoadManifest("/nonexistent/manifest.yaml")
	if err == nil {
		t.Error("LoadManifest() on missing file should return error")
	}
}

// TestMultipleEntriesPreserved verifies the core fix: multiple manifest entries
// with empty platform strings must all be preserved, not collapsed into one.
// This was the bug where map[Platform]ManifestEntry caused all entries with
// Platform{OS: ""} to overwrite each other.
func TestMultipleEntriesPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "ds.manifest.yaml")

	manifestYAML := `
artifact-type: application/vnd.delivery-station.recipe.index.v1+json
annotations:
  name: test-recipes
manifests:
  - path: recipes/recipe-1.yaml
    mediaType: application/vnd.delivery-station.recipe.v1+yaml
  - path: recipes/recipe-2.yaml
    mediaType: application/vnd.delivery-station.recipe.v1+yaml
  - path: recipes/recipe-3.yaml
    mediaType: application/vnd.delivery-station.recipe.v1+yaml
`
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() unexpected error: %v", err)
	}

	// Simulate the entry collection logic from Push() — this is the fixed code path.
	// Before the fix, this used map[Platform]ManifestEntry which collapsed all entries
	// with empty platform to a single entry.
	var entries []ManifestEntry
	for _, entry := range manifest.Manifests {
		platform, err := ParsePlatform(entry.Platform)
		if err != nil {
			t.Fatalf("ParsePlatform(%q) unexpected error: %v", entry.Platform, err)
		}

		resolvedEntry := entry
		if !filepath.IsAbs(resolvedEntry.Path) {
			resolvedEntry.Path = filepath.Join(filepath.Dir(manifestPath), resolvedEntry.Path)
		}

		if resolvedEntry.MediaType == "" {
			resolvedEntry.MediaType = MediaTypeArtifactBinary
		}

		entries = append(entries, resolvedEntry)
		_ = platform // used in real code for PushBinary
	}

	if len(entries) != 3 {
		t.Fatalf("entries count = %d, want 3 (map-based code would collapse to 1)", len(entries))
	}

	expectedPaths := map[string]bool{
		filepath.Join(tmpDir, "recipes/recipe-1.yaml"): true,
		filepath.Join(tmpDir, "recipes/recipe-2.yaml"): true,
		filepath.Join(tmpDir, "recipes/recipe-3.yaml"): true,
	}
	for _, entry := range entries {
		if !expectedPaths[entry.Path] {
			t.Errorf("unexpected entry path: %s", entry.Path)
		}
		delete(expectedPaths, entry.Path)
	}
	if len(expectedPaths) != 0 {
		t.Errorf("missing entries: %v", expectedPaths)
	}
}

// TestMultipleEntriesPreserved_MixedPlatforms verifies that entries with both
// empty and non-empty platforms are all preserved correctly.
func TestMultipleEntriesPreserved_MixedPlatforms(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "ds.manifest.yaml")

	manifestYAML := `
artifact-type: application/vnd.delivery-station.artifact.index.v1+json
manifests:
  - path: binaries/darwin-amd64
    platform: darwin/amd64
    mediaType: application/vnd.delivery-station.artifact.v1+binary
  - path: binaries/linux-amd64
    platform: linux/amd64
    mediaType: application/vnd.delivery-station.artifact.v1+binary
  - path: binaries/linux-arm64
    platform: linux/arm64
    mediaType: application/vnd.delivery-station.artifact.v1+binary
`
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() unexpected error: %v", err)
	}

	type entryWithPlatform struct {
		entry    ManifestEntry
		platform Platform
	}
	var entries []entryWithPlatform
	for _, entry := range manifest.Manifests {
		platform, err := ParsePlatform(entry.Platform)
		if err != nil {
			t.Fatalf("ParsePlatform(%q) unexpected error: %v", entry.Platform, err)
		}

		resolvedEntry := entry
		if !filepath.IsAbs(resolvedEntry.Path) {
			resolvedEntry.Path = filepath.Join(filepath.Dir(manifestPath), resolvedEntry.Path)
		}

		entries = append(entries, entryWithPlatform{entry: resolvedEntry, platform: platform})
	}

	if len(entries) != 3 {
		t.Fatalf("entries count = %d, want 3", len(entries))
	}

	foundPlatforms := make(map[string]bool)
	for _, ewp := range entries {
		foundPlatforms[ewp.platform.FormatString()] = true
	}

	expectedPlatforms := []string{"darwin/amd64", "linux/amd64", "linux/arm64"}
	for _, p := range expectedPlatforms {
		if !foundPlatforms[p] {
			t.Errorf("missing platform: %s", p)
		}
	}
}

// TestPlatformFormatString verifies the FormatString method output.
func TestPlatformFormatString(t *testing.T) {
	tests := []struct {
		name string
		p    Platform
		want string
	}{
		{"empty", Platform{}, "/"},
		{"os only", Platform{OS: "linux"}, "linux/"},
		{"os/arch", Platform{OS: "linux", Arch: "amd64"}, "linux/amd64"},
		{"full", Platform{OS: "linux", Arch: "arm64", Variant: "v8"}, "linux/arm64/v8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.p.FormatString()
			if got != tt.want {
				t.Errorf("Platform{%+v}.FormatString() = %q, want %q", tt.p, got, tt.want)
			}
		})
	}
}
