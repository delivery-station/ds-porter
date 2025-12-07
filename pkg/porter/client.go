package porter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/hashicorp/go-hclog"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// Client handles OCI artifact operations as a DS plugin
type Client struct {
	config *Config
	logger hclog.Logger
}

// Config holds Porter plugin configuration provided by DS
type Config struct {
	Registries []RegistryConfig `json:"registries"`
	CacheDir   string           `json:"cache_dir"`
}

// RegistryConfig holds OCI registry configuration
type RegistryConfig struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
}

// ArtifactResult represents the result of pull/push operations
type ArtifactResult struct {
	ID            string               `json:"id"`
	Reference     string               `json:"reference"`
	Digest        string               `json:"digest"`
	Size          int64                `json:"size"`
	LocalPath     string               `json:"local_path,omitempty"`
	Metadata      map[string]string    `json:"metadata,omitempty"`
	PluginInfo    *PluginExecutionInfo `json:"plugin_info,omitempty"`
	Cached        bool                 `json:"cached"`
	CachedAt      time.Time            `json:"cached_at,omitempty"`
	ExportedFiles []string             `json:"exported_files,omitempty"`
}

// PluginExecutionInfo contains information for executing plugins on artifacts
type PluginExecutionInfo struct {
	PluginName string            `json:"plugin_name"`
	Version    string            `json:"version,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ExportOptions controls how artifacts are materialized to disk.
type ExportOptions struct {
	AllPlatforms       bool
	Platforms          []ocispec.Platform
	UsePlatformSubdirs bool
}

// LoadConfigFromEnv loads Porter configuration from environment variables set by DS
func LoadConfigFromEnv() (*Config, error) {
	configJSON := os.Getenv("DS_PORTER_CONFIG")
	if configJSON == "" {
		// Default configuration
		homeDir, _ := os.UserHomeDir()
		return &Config{
			CacheDir: filepath.Join(homeDir, ".ds", "porter-cache"),
		}, nil
	}

	var config Config
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &config, nil
}

// NewClient creates a new Porter client
func NewClient(cfg *Config, logger hclog.Logger) (*Client, error) {
	if logger == nil {
		logger = hclog.New(&hclog.LoggerOptions{Name: "porter"})
	}

	// Ensure cache directory exists
	if err := os.MkdirAll(cfg.CacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	return &Client{
		config: cfg,
		logger: logger,
	}, nil
}

// PullArtifact pulls an artifact from an OCI registry
func (c *Client) PullArtifact(ref string, insecure bool) (*ArtifactResult, error) {
	c.logger.Info("Pulling artifact", "ref", ref, "insecure", insecure)

	ctx := context.Background()

	// Parse reference to get registry and repo
	// We use go-containerregistry for parsing as it's robust, but we'll use ORAS for pulling
	var opts []name.Option
	if insecure {
		opts = append(opts, name.Insecure)
	}

	var imgRef name.Reference
	var err error

	// Workaround for localhost/ prefix
	if strings.HasPrefix(ref, "localhost/") {
		localOpts := append(opts, name.WithDefaultRegistry("localhost"))
		imgRef, err = name.ParseReference(strings.TrimPrefix(ref, "localhost/"), localOpts...)
	} else {
		imgRef, err = name.ParseReference(ref, opts...)
	}

	if err != nil {
		return nil, fmt.Errorf("invalid reference: %w", err)
	}

	// Setup ORAS repository
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository: %w", err)
	}

	// Configure auth
	client := &auth.Client{
		Client: retry.DefaultClient,
		Cache:  auth.DefaultCache,
	}

	// Try to get credentials from config
	regName := imgRef.Context().RegistryStr()
	for _, r := range c.config.Registries {
		if r.URL == regName || r.Name == regName {
			if r.Username != "" && r.Password != "" {
				client.Credential = auth.StaticCredential(regName, auth.Credential{
					Username: r.Username,
					Password: r.Password,
				})
			} else if r.Token != "" {
				// Bearer token support might need custom credential helper or header injection
				// For now, basic auth is primary
			}
		}
	}
	repo.Client = client
	repo.PlainHTTP = insecure

	// Generate artifact ID based on ref (we don't have digest yet)
	// We'll update it later if needed, but for cache path we need something stable
	// Using hash of ref for now to start cache dir
	artifactID := fmt.Sprintf("%x", sha256.Sum256([]byte(ref)))[:16]
	cachePath := filepath.Join(c.config.CacheDir, artifactID)

	// Create OCI layout store in cache
	store, err := oci.New(cachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create OCI store: %w", err)
	}

	// Pull artifact (recursively if index)
	// We use the tag or digest from ref
	targetRef := imgRef.Identifier()

	c.logger.Info("Copying artifact to cache", "target", targetRef)
	desc, err := oras.Copy(ctx, repo, targetRef, store, targetRef, oras.CopyOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to copy artifact: %w", err)
	}

	// Update artifact ID to include digest for uniqueness if desired,
	// but we already committed to a path.
	// Let's stick with the ID we generated or maybe use digest?
	// If we use digest, we'd need to move the directory.
	// For now, let's keep the ID based on ref hash or just use digest as ID?
	// The previous implementation used ref+digest.
	// Let's use digest as ID to be content-addressable if possible, but we already downloaded to cachePath.
	// We can rename the directory.
	finalArtifactID := desc.Digest.Encoded()[:16]
	finalCachePath := filepath.Join(c.config.CacheDir, finalArtifactID)

	if finalCachePath != cachePath {
		// Check if target exists
		if _, err := os.Stat(finalCachePath); err == nil {
			// Already exists, remove temp
			os.RemoveAll(cachePath)
		} else {
			// Rename
			if err := os.Rename(cachePath, finalCachePath); err != nil {
				// Fallback to original path
				finalArtifactID = artifactID
				finalCachePath = cachePath
			}
		}
	}

	// Read manifest to get metadata
	// We don't strictly need to read it here if we just want annotations from descriptor
	// But if we want to inspect content, we can.
	// manifestBytes, err := content.FetchAll(ctx, store, desc)
	// if err != nil {
	// 	// Might be an index, try to read as index
	// }

	// We need to find metadata. If it's an index, metadata might be on the index or the children.
	metadata := make(map[string]string)
	if desc.Annotations != nil {
		for k, v := range desc.Annotations {
			metadata[k] = v
		}
	}

	// Check for plugin execution info in metadata
	var pluginInfo *PluginExecutionInfo
	if pluginName, ok := metadata["ds.plugin.name"]; ok {
		pluginInfo = &PluginExecutionInfo{
			PluginName: pluginName,
			Version:    metadata["ds.plugin.version"],
			Parameters: make(map[string]string),
		}
		for k, v := range metadata {
			if strings.HasPrefix(k, "ds.plugin.param.") {
				paramName := strings.TrimPrefix(k, "ds.plugin.param.")
				pluginInfo.Parameters[paramName] = v
			}
		}
	}

	result := &ArtifactResult{
		ID:         finalArtifactID,
		Reference:  ref,
		Digest:     desc.Digest.String(),
		Size:       desc.Size,
		LocalPath:  finalCachePath,
		Metadata:   metadata,
		PluginInfo: pluginInfo,
		Cached:     true,
		CachedAt:   time.Now(),
	}

	// Save artifact metadata
	if err := c.saveArtifactMetadata(result); err != nil {
		c.logger.Warn("Failed to save artifact metadata", "error", err)
	}

	c.logger.Info("Artifact pulled successfully",
		"id", finalArtifactID,
		"digest", desc.Digest.String(),
		"size", desc.Size,
	)

	return result, nil
}

// PushArtifact pushes an artifact to an OCI registry
func (c *Client) PushArtifact(artifactPath string, ref string, insecure bool) (*ArtifactResult, error) {
	// TODO: Rewrite PushArtifact to use ORAS as well
	// For now, we'll just return an error since we removed the go-containerregistry imports
	// and the user is focused on Pull/Export
	return nil, fmt.Errorf("PushArtifact not implemented with ORAS yet")
}

// ListCachedArtifacts lists all cached artifacts
func (c *Client) ListCachedArtifacts() ([]*ArtifactResult, error) {
	entries, err := os.ReadDir(c.config.CacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*ArtifactResult{}, nil
		}
		return nil, fmt.Errorf("failed to read cache directory: %w", err)
	}

	var artifacts []*ArtifactResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		artifactID := entry.Name()
		metadata, err := c.loadArtifactMetadata(artifactID)
		if err != nil {
			c.logger.Warn("Failed to load metadata", "artifact", artifactID, "error", err)
			continue
		}

		artifacts = append(artifacts, metadata)
	}

	return artifacts, nil
}

// ExecutePlugin executes a plugin on a cached artifact
func (c *Client) ExecutePlugin(artifactID string, pluginName string, args []string) error {
	c.logger.Info("Executing plugin on artifact",
		"artifact", artifactID,
		"plugin", pluginName,
	)

	// Load artifact metadata
	metadata, err := c.loadArtifactMetadata(artifactID)
	if err != nil {
		return fmt.Errorf("artifact not found: %w", err)
	}

	// Plugin execution is delegated to DS
	// This just logs and returns - DS will handle the actual execution
	c.logger.Info("Plugin execution requested",
		"artifact_path", metadata.LocalPath,
		"plugin", pluginName,
		"args", args,
	)

	return nil
}

// Close cleans up resources
func (c *Client) Close() error {
	return nil
}

// Helper methods

func (c *Client) getAuthForRegistry(registry string) authn.Authenticator {
	for _, reg := range c.config.Registries {
		if reg.URL == registry || reg.Name == registry {
			if reg.Token != "" {
				return &authn.Bearer{Token: reg.Token}
			}
			if reg.Username != "" && reg.Password != "" {
				return &authn.Basic{
					Username: reg.Username,
					Password: reg.Password,
				}
			}
		}
	}
	return authn.Anonymous
}

// ExportArtifact copies the artifact from cache to the destination
func (c *Client) ExportArtifact(result *ArtifactResult, destination string, opts ExportOptions) ([]string, error) {
	if destination == "" {
		return nil, fmt.Errorf("destination required")
	}

	store, err := oci.New(result.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open OCI store: %w", err)
	}

	ctx := context.Background()
	digest := result.Digest
	if digest == "" {
		return nil, fmt.Errorf("artifact digest missing")
	}

	desc, err := store.Resolve(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve artifact descriptor %s: %w", digest, err)
	}
	if desc.Digest.String() == "" {
		return nil, fmt.Errorf("failed to resolve artifact descriptor %s", digest)
	}

	manifests, err := c.selectManifests(ctx, store, desc, opts)
	if err != nil {
		return nil, err
	}
	if len(manifests) == 0 {
		return nil, fmt.Errorf("no matching platform found for export")
	}

	destInfo, err := os.Stat(destination)
	destExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to stat destination: %w", err)
	}

	multiManifest := len(manifests) > 1
	needsSubdirs := opts.UsePlatformSubdirs || multiManifest
	looksFile := destinationLooksLikeFile(destination)

	destIsDir := destExists && destInfo.IsDir()
	destIsFile := destExists && !destIsDir

	if destIsFile && (needsSubdirs || multiManifest) {
		return nil, fmt.Errorf("destination must be a directory when exporting multiple platforms")
	}

	if !destExists {
		if needsSubdirs {
			if err := os.MkdirAll(destination, 0755); err != nil {
				return nil, fmt.Errorf("failed to create destination directory: %w", err)
			}
			destIsDir = true
		} else if looksFile {
			parent := filepath.Dir(destination)
			if parent != "" && parent != "." {
				if err := os.MkdirAll(parent, 0755); err != nil {
					return nil, fmt.Errorf("failed to create parent directory: %w", err)
				}
			}
			destIsFile = true
		} else {
			if err := os.MkdirAll(destination, 0755); err != nil {
				return nil, fmt.Errorf("failed to create destination directory: %w", err)
			}
			destIsDir = true
		}
	}

	baseName := deriveArtifactBaseName(result.Reference)
	var exported []string

	if destIsFile {
		if multiManifest {
			return nil, fmt.Errorf("cannot export multiple manifests to a single file")
		}
		paths, err := c.exportManifestToFile(ctx, store, manifests[0].Descriptor, destination)
		if err != nil {
			return nil, err
		}
		return paths, nil
	}

	// At this point we treat destination as directory (existing or newly created)
	for _, entry := range manifests {
		targetDir := destination
		if needsSubdirs {
			if entry.Platform != nil && entry.Platform.OS != "" && entry.Platform.Architecture != "" {
				platformPath := filepath.Join(destination, entry.Platform.OS, entry.Platform.Architecture)
				if entry.Platform.Variant != "" {
					platformPath = filepath.Join(platformPath, entry.Platform.Variant)
				}
				targetDir = platformPath
			} else {
				targetDir = filepath.Join(destination, "unknown")
			}
		}

		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create destination directory: %w", err)
		}

		paths, err := c.exportManifestLayers(ctx, store, entry.Descriptor, targetDir, baseName, entry.Platform)
		if err != nil {
			return nil, err
		}
		exported = append(exported, paths...)
	}

	return exported, nil
}

type manifestSelection struct {
	Descriptor ocispec.Descriptor
	Platform   *ocispec.Platform
}

func (c *Client) selectManifests(ctx context.Context, store *oci.Store, root ocispec.Descriptor, opts ExportOptions) ([]manifestSelection, error) {
	if isIndexDescriptor(root) {
		indexBytes, err := content.FetchAll(ctx, store, root)
		if err != nil {
			return nil, fmt.Errorf("failed to read index: %w", err)
		}
		var index ocispec.Index
		if err := json.Unmarshal(indexBytes, &index); err != nil {
			return nil, fmt.Errorf("failed to parse index: %w", err)
		}

		var selections []manifestSelection
		for _, manifest := range index.Manifests {
			if opts.AllPlatforms || platformMatches(manifest.Platform, opts.Platforms) {
				selections = append(selections, manifestSelection{Descriptor: manifest, Platform: manifest.Platform})
			}
		}

		if len(selections) == 0 && !opts.AllPlatforms && len(opts.Platforms) > 0 {
			return nil, fmt.Errorf("no manifests found for requested platform(s)")
		}

		return selections, nil
	}

	return []manifestSelection{{Descriptor: root, Platform: root.Platform}}, nil
}

func (c *Client) destinationRequiresDirectory(manifests []manifestSelection, opts ExportOptions) bool {
	if opts.AllPlatforms {
		return true
	}
	if len(manifests) > 1 {
		return true
	}
	if len(manifests) == 1 {
		platform := manifests[0].Platform
		if platform != nil && (platform.OS != "" || platform.Architecture != "") {
			return true
		}
	}
	return false
}

func (c *Client) exportManifestToFile(ctx context.Context, store *oci.Store, manifestDesc ocispec.Descriptor, destination string) ([]string, error) {
	manifestBytes, err := content.FetchAll(ctx, store, manifestDesc)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	if len(manifest.Layers) != 1 {
		return nil, fmt.Errorf("expected a single layer, found %d", len(manifest.Layers))
	}

	layer := manifest.Layers[0]
	layerReader, err := store.Fetch(ctx, layer)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch layer: %w", err)
	}
	defer layerReader.Close()

	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return nil, fmt.Errorf("failed to create destination path: %w", err)
	}

	outFile, err := os.Create(destination)
	if err != nil {
		return nil, fmt.Errorf("failed to create destination file: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, layerReader); err != nil {
		return nil, fmt.Errorf("failed to copy layer: %w", err)
	}

	c.logger.Info("Exported layer", "digest", layer.Digest, "path", destination)
	return []string{destination}, nil
}

func (c *Client) exportManifestLayers(ctx context.Context, store *oci.Store, manifestDesc ocispec.Descriptor, destDir, baseName string, platform *ocispec.Platform) ([]string, error) {
	manifestBytes, err := content.FetchAll(ctx, store, manifestDesc)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	var exported []string
	for _, layer := range manifest.Layers {
		filename := determineLayerFilename(layer, baseName, platform)
		destPath := filepath.Join(destDir, filename)

		layerReader, err := store.Fetch(ctx, layer)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch layer: %w", err)
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			layerReader.Close()
			return nil, fmt.Errorf("failed to create destination directory: %w", err)
		}

		outFile, err := os.Create(destPath)
		if err != nil {
			layerReader.Close()
			return nil, fmt.Errorf("failed to create file: %w", err)
		}

		if _, err := io.Copy(outFile, layerReader); err != nil {
			outFile.Close()
			layerReader.Close()
			return nil, fmt.Errorf("failed to copy layer: %w", err)
		}

		outFile.Close()
		layerReader.Close()

		exported = append(exported, destPath)
		c.logger.Info("Exported layer", "digest", layer.Digest, "path", destPath)
	}

	return exported, nil
}

func destinationLooksLikeFile(path string) bool {
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		return false
	}
	return filepath.Ext(path) != ""
}

func determineLayerFilename(layer ocispec.Descriptor, baseName string, platform *ocispec.Platform) string {
	if title, ok := layer.Annotations[ocispec.AnnotationTitle]; ok {
		trimmed := strings.TrimSpace(title)
		if trimmed != "" {
			return sanitizeFilename(trimmed)
		}
	}

	name := baseName
	if name == "" {
		name = "artifact"
	}

	ext := filepath.Ext(name)
	if ext == "" {
		ext = defaultExtension(layer, platform)
		if ext != "" && !strings.HasSuffix(name, ext) {
			name += ext
		}
	}

	return sanitizeFilename(name)
}

func defaultExtension(layer ocispec.Descriptor, platform *ocispec.Platform) string {
	if platform != nil && strings.EqualFold(platform.OS, "windows") {
		return ".exe"
	}
	if strings.Contains(layer.MediaType, "tar+gzip") {
		return ".tar.gz"
	}
	return ""
}

func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer("\\", "-", "/", "-", ":", "-", " ", "-")
	clean := replacer.Replace(name)
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return "artifact"
	}
	return clean
}

func deriveArtifactBaseName(ref string) string {
	name := ref
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if idx := strings.IndexAny(name, "@:"); idx >= 0 {
		name = name[:idx]
	}
	return sanitizeFilename(name)
}

func platformMatches(platform *ocispec.Platform, targets []ocispec.Platform) bool {
	if len(targets) == 0 {
		return true
	}
	if platform == nil {
		return len(targets) == 1
	}
	for _, target := range targets {
		if !strings.EqualFold(target.OS, platform.OS) {
			continue
		}
		if !strings.EqualFold(target.Architecture, platform.Architecture) {
			continue
		}
		if target.Variant == "" || strings.EqualFold(target.Variant, platform.Variant) {
			return true
		}
	}
	return false
}

func isIndexDescriptor(desc ocispec.Descriptor) bool {
	return desc.MediaType == ocispec.MediaTypeImageIndex || desc.MediaType == "application/vnd.oci.image.index.v1+json"
}

func (c *Client) saveArtifactMetadata(artifact *ArtifactResult) error {
	metadataPath := filepath.Join(c.config.CacheDir, artifact.ID, "metadata.json")

	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	if err := os.WriteFile(metadataPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

func (c *Client) loadArtifactMetadata(artifactID string) (*ArtifactResult, error) {
	metadataPath := filepath.Join(c.config.CacheDir, artifactID, "metadata.json")

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var artifact ArtifactResult
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	return &artifact, nil
}
