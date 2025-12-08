package release

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"gopkg.in/yaml.v3"
)

// Manifest represents the ds.manifest.yaml structure
type Manifest struct {
	ArtifactType string            `yaml:"artifact-type"`
	Annotations  map[string]string `yaml:"annotations"`
	Manifests    []ManifestEntry   `yaml:"manifests"`
}

// ManifestEntry represents a platform entry in the manifest
type ManifestEntry struct {
	Platform  string `yaml:"platform"`
	MediaType string `yaml:"mediaType"`
	Path      string `yaml:"path"`
}

// LoadManifest reads and parses the manifest file
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	return &manifest, nil
}

// ParsePlatform parses a platform string (os/arch/variant)
func ParsePlatform(s string) (Platform, error) {
	// Format: os[/arch][/variant][:os_version]
	// Remove os_version for now as we don't use it for build
	if idx := strings.Index(s, ":"); idx != -1 {
		s = s[:idx]
	}

	parts := strings.Split(s, "/")
	p := Platform{
		OS: parts[0],
	}

	if len(parts) > 1 {
		p.Arch = parts[1]
	}
	if len(parts) > 2 {
		p.Variant = parts[2]
	}

	return p, nil
}

// Platform represents a target build platform
type Platform struct {
	OS      string
	Arch    string
	Variant string
}

// BuildConfig contains configuration for multi-arch builds
type BuildConfig struct {
	Version    string
	BinaryName string
	SourceDir  string
	OutputDir  string
	LDFlags    string
	Commit     string
}

// ReleaseConfig contains configuration for OCI registry push
type ReleaseConfig struct {
	Reference    string
	Username     string
	Password     string
	TagLatest    bool
	ManifestPath string
	Insecure     bool
}

// Release orchestrates building and publishing multi-arch artifacts.
type Release struct {
	buildConfig BuildConfig
	publisher   *Pusher
}

// Pusher handles pushing artifacts to OCI registry
type Pusher struct {
	config ReleaseConfig
	client *auth.Client
}

// NewPusher creates a new Pusher
func NewPusher(config ReleaseConfig) (*Pusher, error) {
	client := &auth.Client{
		Client: retry.DefaultClient,
		Cache:  auth.DefaultCache,
	}

	if config.Username != "" && config.Password != "" {
		// Parse registry from reference
		// Assuming reference is registry/repo[:tag]
		parts := strings.SplitN(config.Reference, "/", 2)
		if len(parts) > 0 {
			client.Credential = auth.StaticCredential(parts[0], auth.Credential{
				Username: config.Username,
				Password: config.Password,
			})
		}
	}

	return &Pusher{
		config: config,
		client: client,
	}, nil
}

func writeProgressLine(progress io.Writer, format string, args ...interface{}) error {
	if progress == nil {
		return nil
	}
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	if _, err := fmt.Fprintf(progress, format, args...); err != nil {
		return fmt.Errorf("failed to write progress output: %w", err)
	}
	return nil
}

// Push performs the multi-arch push
func (p *Pusher) Push(ctx context.Context, progress io.Writer) error {
	if err := writeProgressLine(progress, "=== Porter Plugin Multi-Arch Push ==="); err != nil {
		return err
	}
	if err := writeProgressLine(progress, ""); err != nil {
		return err
	}

	// Load manifest
	if err := writeProgressLine(progress, "Loading manifest from %s...", p.config.ManifestPath); err != nil {
		return err
	}
	manifest, err := LoadManifest(p.config.ManifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	// Push artifacts
	entries := make(map[Platform]ManifestEntry)
	for _, entry := range manifest.Manifests {
		platform, err := ParsePlatform(entry.Platform)
		if err != nil {
			return fmt.Errorf("invalid platform %s: %w", entry.Platform, err)
		}

		if entry.Path == "" {
			return fmt.Errorf("path required for platform %s", entry.Platform)
		}

		entries[platform] = entry
	}

	if err := writeProgressLine(progress, "Pushing artifacts to OCI registry..."); err != nil {
		return err
	}
	descriptors, err := p.PushAll(ctx, entries, progress)
	if err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	// Push Index
	if err := writeProgressLine(progress, "Pushing manifest index..."); err != nil {
		return err
	}
	ref, err := p.PushIndex(ctx, descriptors, manifest)
	if err != nil {
		return fmt.Errorf("push index failed: %w", err)
	}

	if err := writeProgressLine(progress, ""); err != nil {
		return err
	}
	if err := writeProgressLine(progress, "✓ Pushed to %s", ref); err != nil {
		return err
	}

	return nil
}

// PushAll pushes all platform binaries and creates a multi-arch manifest
func (p *Pusher) PushAll(ctx context.Context, entries map[Platform]ManifestEntry, progress io.Writer) (map[Platform]ocispec.Descriptor, error) {
	descriptors := make(map[Platform]ocispec.Descriptor)

	// Push each platform binary
	for platform, entry := range entries {
		if err := writeProgressLine(progress, "Pushing %s/%s...", platform.OS, platform.Arch); err != nil {
			return nil, err
		}

		desc, err := p.PushBinary(ctx, platform, entry)
		if err != nil {
			return nil, fmt.Errorf("failed to push %s/%s: %w", platform.OS, platform.Arch, err)
		}

		descriptors[platform] = desc
		if err := writeProgressLine(progress, "✓ Pushed %s → %s", platform.FormatString(), desc.Digest); err != nil {
			return nil, err
		}
	}

	if err := writeProgressLine(progress, "✓ All platform binaries pushed successfully"); err != nil {
		return nil, err
	}
	return descriptors, nil
}

// PushBinary pushes a single platform binary to the registry
func (p *Pusher) PushBinary(ctx context.Context, platform Platform, entry ManifestEntry) (ocispec.Descriptor, error) {
	binaryPath := entry.Path

	// Create hybrid store
	store := NewFileStore()

	// Add binary file to store (calculates digest, doesn't copy)
	binaryDesc, err := store.AddFile(binaryPath, "application/octet-stream")
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to add binary to store: %w", err)
	}

	// Create artifact manifest
	artifactType := "application/vnd.delivery-station.plugin.v1+binary"
	if entry.MediaType != "" {
		artifactType = entry.MediaType
	}
	opts := oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{binaryDesc},
	}

	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, artifactType, opts)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to pack manifest: %w", err)
	}

	// Add annotations to manifest
	manifestDesc.Annotations = map[string]string{
		ocispec.AnnotationCreated: time.Now().UTC().Format(time.RFC3339),
		"os":                      platform.OS,
		"architecture":            platform.Arch,
	}
	if platform.Variant != "" {
		manifestDesc.Annotations["variant"] = platform.Variant
	}

	// Push to remote registry by digest
	// We use the base reference (repo) and push the manifest by digest
	baseRef := p.config.Reference
	if !strings.Contains(baseRef, ":") {
		baseRef += ":latest"
	}
	parts := strings.Split(baseRef, ":")
	baseTag := parts[len(parts)-1]
	repoName := strings.TrimSuffix(baseRef, ":"+baseTag)

	repo, err := remote.NewRepository(repoName)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create repository: %w", err)
	}
	repo.Client = p.client
	repo.PlainHTTP = p.config.Insecure

	// Push manifest and blobs
	if _, err := oras.Copy(ctx, store, manifestDesc.Digest.String(), repo, manifestDesc.Digest.String(), oras.CopyOptions{}); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to copy to registry: %w", err)
	}

	return manifestDesc, nil
}

// PushIndex creates and pushes the multi-arch manifest index
func (p *Pusher) PushIndex(ctx context.Context, descriptors map[Platform]ocispec.Descriptor, manifest *Manifest) (string, error) {
	// Create memory store for index
	store := memory.New()

	var layers []ocispec.Descriptor

	// Base reference
	baseRef := p.config.Reference
	if !strings.Contains(baseRef, ":") {
		baseRef += ":latest"
	}

	// Extract tag
	parts := strings.Split(baseRef, ":")
	baseTag := parts[len(parts)-1]
	repoName := strings.TrimSuffix(baseRef, ":"+baseTag)

	repo, err := remote.NewRepository(repoName)
	if err != nil {
		return "", fmt.Errorf("failed to create repository: %w", err)
	}
	repo.Client = p.client
	repo.PlainHTTP = p.config.Insecure

	for platform, desc := range descriptors {
		// Add platform info to descriptor
		desc.Platform = &ocispec.Platform{
			OS:           platform.OS,
			Architecture: platform.Arch,
			Variant:      platform.Variant,
		}
		layers = append(layers, desc)
	}

	// Create index manifest
	artifactType := "application/vnd.delivery-station.plugin.index.v1+json"
	if manifest != nil && manifest.ArtifactType != "" {
		artifactType = manifest.ArtifactType
	}

	// Construct OCI Index
	index := ocispec.Index{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: layers,
	}

	// Add annotations
	if manifest != nil {
		index.Annotations = manifest.Annotations
	}

	// Set ArtifactType if provided (OCI v1.1)
	if artifactType != "" {
		index.ArtifactType = artifactType
	}

	// Marshal index
	indexBytes, err := json.Marshal(index)
	if err != nil {
		return "", fmt.Errorf("failed to marshal index: %w", err)
	}

	// Tag the index
	tag := baseTag
	indexDesc := ocispec.Descriptor{
		MediaType: index.MediaType,
		Digest:    digest.FromBytes(indexBytes),
		Size:      int64(len(indexBytes)),
	}
	if err := store.Push(ctx, indexDesc, bytes.NewReader(indexBytes)); err != nil {
		return "", fmt.Errorf("failed to add index to store: %w", err)
	}
	if err := store.Tag(ctx, indexDesc, tag); err != nil {
		return "", fmt.Errorf("failed to tag index: %w", err)
	}

	// Push index
	_, err = oras.Copy(ctx, store, tag, repo, tag, oras.CopyOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to push index: %w", err)
	}

	if p.config.TagLatest && tag != "latest" {
		if err := repo.Tag(ctx, indexDesc, "latest"); err != nil {
			return "", fmt.Errorf("failed to tag latest: %w", err)
		}
	}

	return baseRef, nil
}

// NewRelease creates a new Release orchestrator
func NewRelease(buildConfig BuildConfig, releaseConfig ReleaseConfig) (*Release, error) {
	publisher, err := NewPusher(releaseConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}

	return &Release{
		buildConfig: buildConfig,
		publisher:   publisher,
	}, nil
}

// Execute performs the complete build and release process
func (r *Release) Execute(ctx context.Context, stdout, stderr io.Writer) error {
	return r.publisher.Push(ctx, stdout)
}

// FormatString returns a formatted string representation of the platform
func (p Platform) FormatString() string {
	if p.OS == "noarch" {
		return "noarch"
	}
	if p.Variant != "" {
		return fmt.Sprintf("%s/%s/%s", p.OS, p.Arch, p.Variant)
	}
	return fmt.Sprintf("%s/%s", p.OS, p.Arch)
}

// TagSuffix returns the tag suffix for this platform
func (p Platform) TagSuffix() string {
	if p.OS == "noarch" {
		return "noarch"
	}
	suffix := fmt.Sprintf("%s-%s", p.OS, p.Arch)
	if p.Variant != "" {
		suffix += "-" + p.Variant
	}
	return suffix
}

// GetCurrentPlatform returns the current OS/Arch
func GetCurrentPlatform() Platform {
	return Platform{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
}

// FileStore is a hybrid store that serves files from disk and other content from memory
type FileStore struct {
	*memory.Store
	files map[string]fileEntry // digest -> entry
}

type fileEntry struct {
	path string
	desc ocispec.Descriptor
}

// NewFileStore creates a new FileStore
func NewFileStore() *FileStore {
	return &FileStore{
		Store: memory.New(),
		files: make(map[string]fileEntry),
	}
}

// Fetch retrieves content from disk or memory
func (s *FileStore) Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	if entry, ok := s.files[target.Digest.String()]; ok {
		if entry.path != "" {
			return os.Open(entry.path)
		}
	}
	return s.Store.Fetch(ctx, target)
}

// Resolve resolves a reference to a descriptor
func (s *FileStore) Resolve(ctx context.Context, ref string) (ocispec.Descriptor, error) {
	// Check if ref is a digest we have
	if entry, ok := s.files[ref]; ok {
		return entry.desc, nil
	}
	// Also try parsing ref as digest
	d, err := digest.Parse(ref)
	if err == nil {
		if entry, ok := s.files[d.String()]; ok {
			return entry.desc, nil
		}
	}

	return s.Store.Resolve(ctx, ref)
}

// Push pushes content to the store
func (s *FileStore) Push(ctx context.Context, expected ocispec.Descriptor, content io.Reader) error {
	// Store the descriptor in our map so Resolve can find it
	s.files[expected.Digest.String()] = fileEntry{
		path: "", // No path for memory content
		desc: expected,
	}

	return s.Store.Push(ctx, expected, content)
}

// AddFile adds a file to the store map and returns its descriptor
func (s *FileStore) AddFile(path string, mediaType string) (ocispec.Descriptor, error) {
	f, err := os.Open(path)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	defer func() {
		_ = f.Close()
	}()

	stat, err := f.Stat()
	if err != nil {
		return ocispec.Descriptor{}, err
	}

	d, err := digest.FromReader(f)
	if err != nil {
		return ocispec.Descriptor{}, err
	}

	desc := ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    d,
		Size:      stat.Size(),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: filepath.Base(path),
		},
	}

	s.files[d.String()] = fileEntry{
		path: path,
		desc: desc,
	}
	return desc, nil
}
