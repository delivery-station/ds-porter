package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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

	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"

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
	Platform    string            `yaml:"platform"`
	MediaType   string            `yaml:"mediaType"`
	Path        string            `yaml:"path"`
	Annotations map[string]string `yaml:"annotations"`
}

// Delivery Station media types for general artifacts.
const (
	MediaTypeArtifactBinary  = "application/vnd.delivery-station.artifact.v1+binary"
	MediaTypeArtifactArchive = "application/vnd.delivery-station.artifact.v1+archive.tar+gzip"
	MediaTypeArtifactIndex   = "application/vnd.delivery-station.artifact.index.v1+json"

	// New Plugin specific types
	MediaTypePluginConfig = "application/vnd.delivery-station.plugin.config.v1+json"
	ArtifactTypePlugin    = "application/vnd.delivery-station.plugin.v1+json"
)

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

	Insecure bool
	Signing  SigningConfig
}

// SigningConfig contains settings for signing artifacts
type SigningConfig struct {
	Enabled    bool
	PrivateKey string
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

	manifestDir := filepath.Dir(p.config.ManifestPath)

	// Push artifacts
	if err := writeProgressLine(progress, "Pushing artifacts to OCI registry..."); err != nil {
		return err
	}

	// Prepare artifact entries
	var entries []ManifestEntry
	for _, entry := range manifest.Manifests {
		platform, err := ParsePlatform(entry.Platform)
		if err != nil {
			return fmt.Errorf("invalid platform %s: %w", entry.Platform, err)
		}

		if strings.TrimSpace(entry.Path) == "" {
			return fmt.Errorf("path required for platform %s", platform.FormatString())
		}

		resolvedEntry := entry
		if !filepath.IsAbs(resolvedEntry.Path) {
			resolvedEntry.Path = filepath.Join(manifestDir, resolvedEntry.Path)
		}

		if strings.TrimSpace(resolvedEntry.MediaType) == "" {
			resolvedEntry.MediaType = MediaTypeArtifactBinary
		}

		entries = append(entries, resolvedEntry)
	}

	// Push artifacts (Manifests wrapped around Layers)
	descriptors, _, err := p.PushAll(ctx, entries, progress)
	if err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	// NOTE: Global SHA256SUMS signing is removed in favor of per-layer signing
	// The signatures are now embedded in the Layer Annotations within each Manifest.

	// Push Index
	if err := writeProgressLine(progress, "Pushing manifest index..."); err != nil {
		return err
	}
	ref, err := p.PushIndex(ctx, descriptors, entries, manifest)
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
func (p *Pusher) PushAll(ctx context.Context, entries []ManifestEntry, progress io.Writer) ([]ocispec.Descriptor, []ocispec.Descriptor, error) {
	descriptors := make([]ocispec.Descriptor, 0, len(entries))
	layers := make([]ocispec.Descriptor, 0, len(entries))

	// Push each entry
	for _, entry := range entries {
		platform, err := ParsePlatform(entry.Platform)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid platform %s: %w", entry.Platform, err)
		}

		if err := writeProgressLine(progress, "Pushing %s/%s...", platform.OS, platform.Arch); err != nil {
			return nil, nil, err
		}

		// We only need the manifest descriptor for the index
		desc, _, err := p.PushBinary(ctx, platform, entry)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to push %s/%s: %w", platform.OS, platform.Arch, err)
		}

		descriptors = append(descriptors, desc)
		layers = append(layers, desc)
		if err := writeProgressLine(progress, "✓ Pushed %s → %s", platform.FormatString(), desc.Digest); err != nil {
			return nil, nil, err
		}
	}

	if err := writeProgressLine(progress, "✓ All platform binaries pushed successfully"); err != nil {
		return nil, nil, err
	}
	return descriptors, layers, nil
}

// PushBinary pushes a single platform binary to the registry
func (p *Pusher) PushBinary(ctx context.Context, platform Platform, entry ManifestEntry) (ocispec.Descriptor, ocispec.Descriptor, error) {
	binaryPath := entry.Path

	info, err := os.Stat(binaryPath)
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to stat %s: %w", binaryPath, err)
	}

	var cleanup func()
	if info.IsDir() {
		archivePath, archiveCleanup, archiveErr := archiveDirectory(binaryPath)
		if archiveErr != nil {
			return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to archive directory %s: %w", binaryPath, archiveErr)
		}
		cleanup = archiveCleanup
		binaryPath = archivePath
		if strings.TrimSpace(entry.MediaType) == "" {
			entry.MediaType = MediaTypeArtifactArchive
		}
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Create hybrid store
	store := NewFileStore()

	// Add binary file to store (calculates digest, doesn't copy)
	layerMediaType := entry.MediaType
	if strings.TrimSpace(layerMediaType) == "" {
		layerMediaType = MediaTypeArtifactBinary
	}
	binaryDesc, err := store.AddFile(binaryPath, layerMediaType)
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to add binary to store: %w", err)
	}

	// Add annotations to binary descriptor
	annotations := map[string]string{
		ocispec.AnnotationCreated: time.Now().UTC().Format(time.RFC3339),
	}
	if strings.TrimSpace(platform.OS) != "" {
		annotations["os"] = platform.OS
	}
	if strings.TrimSpace(platform.Arch) != "" {
		annotations["architecture"] = platform.Arch
	}
	if strings.TrimSpace(platform.Variant) != "" {
		annotations["variant"] = platform.Variant
	}

	// Merge per-entry annotations from manifest (e.g. recipe metadata)
	if entry.Annotations != nil {
		for k, v := range entry.Annotations {
			annotations[k] = v
		}
	}

	binaryDesc.Annotations = annotations

	// Sign binary content if enabled
	if p.config.Signing.Enabled {
		// We need to read the content to sign it
		blobContent, err := store.Fetch(ctx, binaryDesc)
		if err != nil {
			return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to fetch blob content for signing: %w", err)
		}

		// Read fully to memory for signing
		contentBytes, err := io.ReadAll(blobContent)
		if err := blobContent.Close(); err != nil {
			return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to close blob content: %w", err)
		}
		if err != nil {
			return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to read blob content for signing: %w", err)
		}

		signature, keyID, _, err := p.signContent(contentBytes)
		if err != nil {
			return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to sign blob content: %w", err)
		}

		// Add signature annotations to the LAYER descriptor
		binaryDesc.Annotations["delivery-station.io/sha256"] = binaryDesc.Digest.Hex()
		binaryDesc.Annotations["delivery-station.io/signature"] = base64.StdEncoding.EncodeToString(signature)
		binaryDesc.Annotations["delivery-station.io/key.id"] = keyID
	}

	// Push repository
	baseRef := p.config.Reference
	if !strings.Contains(baseRef, ":") {
		baseRef += ":latest"
	}
	parts := strings.Split(baseRef, ":")
	baseTag := parts[len(parts)-1]
	repoName := strings.TrimSuffix(baseRef, ":"+baseTag)

	repo, err := remote.NewRepository(repoName)
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to create repository: %w", err)
	}
	repo.Client = p.client
	repo.PlainHTTP = p.config.Insecure

	// 1. Push Binary Blob
	blobContent, err := store.Fetch(ctx, binaryDesc)
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to fetch blob content: %w", err)
	}
	defer func() { _ = blobContent.Close() }()

	if err := repo.Blobs().Push(ctx, binaryDesc, blobContent); err != nil {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to push blob to registry: %w", err)
	}

	// 2. Create and Push Config Blob
	configBytes := []byte("{}")
	configDesc := ocispec.Descriptor{
		MediaType: MediaTypePluginConfig, // Custom config media type
		Digest:    digest.FromBytes(configBytes),
		Size:      int64(len(configBytes)),
	}
	if err := repo.Blobs().Push(ctx, configDesc, bytes.NewReader(configBytes)); err != nil {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to push config blob: %w", err)
	}

	// 3. Create OCI Manifest
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactTypePlugin, // Explicit ArtifactType
		Config:       configDesc,
		Layers:       []ocispec.Descriptor{binaryDesc},
		Annotations: map[string]string{
			ocispec.AnnotationCreated: time.Now().UTC().Format(time.RFC3339),
		},
	}

	// Pack manifest
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to marshal manifest: %w", err)
	}

	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestBytes),
		Size:      int64(len(manifestBytes)),
		Platform: &ocispec.Platform{
			OS:           platform.OS,
			Architecture: platform.Arch,
			Variant:      platform.Variant,
		},
	}

	// 4. Push Manifest
	if err := repo.Manifests().Push(ctx, manifestDesc, bytes.NewReader(manifestBytes)); err != nil {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, fmt.Errorf("failed to push manifest: %w", err)
	}

	// Return the MANIFEST descriptor (standard OCI structure)
	// We return nil for the second descriptor as we no longer return the layer separately for indexing
	return manifestDesc, ocispec.Descriptor{}, nil
}

// PushIndex creates and pushes the multi-arch manifest index
func (p *Pusher) PushIndex(ctx context.Context, descriptors []ocispec.Descriptor, entries []ManifestEntry, manifest *Manifest) (string, error) {
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

	for i, desc := range descriptors {
		// All descriptors from PushAll have nil Platform (no platform field in manifest)
		desc.Platform = nil

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

	// Create index manifest
	artifactType := MediaTypeArtifactIndex
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
	if manifest != nil && len(manifest.Annotations) > 0 {
		indexDesc.Annotations = manifest.Annotations
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

func archiveDirectory(dir string) (string, func(), error) {
	info, err := os.Stat(dir)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("path %s is not a directory", dir)
	}

	archiveFile, err := os.CreateTemp("", "ds-porter-archive-*.tar.gz")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temporary archive: %w", err)
	}

	gzipWriter := gzip.NewWriter(archiveFile)
	tarWriter := tar.NewWriter(gzipWriter)

	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = archiveFile.Close()
		_ = os.Remove(archiveFile.Name())
		return "", nil, fmt.Errorf("failed to resolve directory %s: %w", dir, err)
	}

	walkErr := filepath.WalkDir(dirAbs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, relErr := filepath.Rel(dirAbs, path)
		if relErr != nil {
			return relErr
		}
		if relPath == "." {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}

		header, headerErr := tar.FileInfoHeader(info, "")
		if headerErr != nil {
			return headerErr
		}
		header.Name = filepath.ToSlash(relPath)

		if d.Type()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return linkErr
			}
			header.Linkname = target
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		if _, copyErr := io.Copy(tarWriter, file); copyErr != nil {
			_ = file.Close()
			return copyErr
		}
		return file.Close()
	})

	firstErr := walkErr
	if closeErr := tarWriter.Close(); firstErr == nil {
		firstErr = closeErr
	}
	if gzipErr := gzipWriter.Close(); firstErr == nil {
		firstErr = gzipErr
	}
	if closeErr := archiveFile.Close(); firstErr == nil {
		firstErr = closeErr
	}

	if firstErr != nil {
		_ = os.Remove(archiveFile.Name())
		return "", nil, fmt.Errorf("failed to archive directory %s: %w", dir, firstErr)
	}

	archivePath := archiveFile.Name()
	return archivePath, func() {
		_ = os.Remove(archivePath)
	}, nil
}

func (p *Pusher) signContent(content []byte) ([]byte, string, string, error) {
	if p.config.Signing.PrivateKey == "" {
		return nil, "", "", fmt.Errorf("private key is required for signing")
	}

	// Parse private key
	// Support both raw PEM content and file path
	var keyBytes []byte
	if strings.Contains(p.config.Signing.PrivateKey, "BEGIN RSA PRIVATE KEY") || strings.Contains(p.config.Signing.PrivateKey, "BEGIN PRIVATE KEY") {
		keyBytes = []byte(p.config.Signing.PrivateKey)
	} else {
		// Try to read from file
		var err error
		keyBytes, err = os.ReadFile(p.config.Signing.PrivateKey)
		if err != nil {
			return nil, "", "", fmt.Errorf("failed to read private key file: %w", err)
		}
	}

	block, _ := pem.Decode(keyBytes)
	if block == nil {
		return nil, "", "", fmt.Errorf("failed to decode PEM block containing private key")
	}

	var privKey *rsa.PrivateKey
	var err error

	if block.Type == "RSA PRIVATE KEY" {
		privKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			privKey, ok = key.(*rsa.PrivateKey)
			if !ok {
				return nil, "", "", fmt.Errorf("private key must be RSA")
			}
		}
	}

	if err != nil {
		return nil, "", "", fmt.Errorf("failed to parse private key: %w", err)
	}

	// Calculate hash of content
	hashed := sha256.Sum256(content)

	// Sign
	signature, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, hashed[:])
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to sign content: %w", err)
	}

	// Generate Public Key Metadata
	pubKey := privKey.Public()
	rsaPubKey, ok := pubKey.(*rsa.PublicKey)
	if !ok {
		return nil, "", "", fmt.Errorf("public key is not RSA")
	}

	pubKeyBytes, err := x509.MarshalPKIXPublicKey(rsaPubKey)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to marshal public key: %w", err)
	}

	// Calculate Key ID (SHA256 fingerprint of PKIX bytes)
	keyHash := sha256.Sum256(pubKeyBytes)
	keyID := fmt.Sprintf("%x", keyHash)

	// ASCII Armor
	pubKeyBlock := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubKeyBytes,
	}
	asciiArmor := string(pem.EncodeToMemory(pubKeyBlock))

	return signature, keyID, asciiArmor, nil
}
