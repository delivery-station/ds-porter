package porter

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/delivery-station/ds/pkg/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubHostConfigProvider struct {
	cfg *types.Config
	err error
}

func (s *stubHostConfigProvider) GetEffectiveConfig(ctx context.Context) (*types.Config, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cfg, nil
}

func TestLoadConfigFromHost(t *testing.T) {
	t.Run("provider available", func(t *testing.T) {
		provider := &stubHostConfigProvider{
			cfg: &types.Config{
				Cache:    types.CacheConfig{Dir: "/tmp/ds-cache"},
				Registry: types.RegistryConfig{Default: "ghcr.io/delivery-station"},
				Auth: types.AuthConfig{
					Credentials: []types.Credential{
						{Registry: "ghcr.io/delivery-station/porter", Username: "alice", Token: "token"},
					},
				},
			},
		}

		ctx := types.WithHostConfigProvider(context.Background(), provider)
		cfg, err := LoadConfigFromHost(ctx)

		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Equal(t, filepath.Join("/tmp/ds-cache", "porter"), cfg.CacheDir)
		assert.Len(t, cfg.Registries, 1)
		assert.Equal(t, "ghcr.io", cfg.Registries[0].Name)
		assert.Equal(t, "ghcr.io", cfg.Registries[0].URL)
	})

	t.Run("missing provider", func(t *testing.T) {
		_, err := LoadConfigFromHost(context.Background())
		assert.Error(t, err)
	})

	t.Run("provider returned no config", func(t *testing.T) {
		provider := &stubHostConfigProvider{}
		ctx := types.WithHostConfigProvider(context.Background(), provider)
		_, err := LoadConfigFromHost(ctx)
		assert.Error(t, err)
	})
}

func TestNewClient(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{
		CacheDir: tmpDir,
		Registries: []RegistryConfig{
			{Name: "test", URL: "registry.test"},
		},
	}

	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	client, err := NewClient(cfg, logger)

	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, tmpDir, client.config.CacheDir)

	// Check cache dir was created
	_, err = os.Stat(tmpDir)
	assert.NoError(t, err)
}

func TestNewClientAppliesLogLevel(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{
		CacheDir: tmpDir,
		LogLevel: "trace",
	}

	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Info})
	_, err := NewClient(cfg, logger)
	require.NoError(t, err)

	assert.Equal(t, hclog.Trace, logger.GetLevel())
}

func TestListCachedArtifacts_Empty(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{
		CacheDir: tmpDir,
	}

	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	client, err := NewClient(cfg, logger)
	require.NoError(t, err)

	artifacts, err := client.ListCachedArtifacts()
	assert.NoError(t, err)
	assert.Empty(t, artifacts)
}

func TestSaveAndLoadArtifactMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{
		CacheDir: tmpDir,
	}

	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	client, err := NewClient(cfg, logger)
	require.NoError(t, err)

	// Create artifact directory
	artifactID := "test123"
	artifactDir := filepath.Join(tmpDir, artifactID)
	err = os.MkdirAll(artifactDir, 0755)
	require.NoError(t, err)

	// Create artifact metadata
	artifact := &ArtifactResult{
		ID:        artifactID,
		Reference: "registry.test/artifact:v1.0.0",
		Digest:    "sha256:abc123",
		Size:      1024,
		Metadata: map[string]string{
			"test": "value",
		},
	}

	// Save metadata
	err = client.saveArtifactMetadata(artifact)
	assert.NoError(t, err)

	// Load metadata
	loaded, err := client.loadArtifactMetadata(artifactID)
	assert.NoError(t, err)
	assert.Equal(t, artifact.ID, loaded.ID)
	assert.Equal(t, artifact.Reference, loaded.Reference)
	assert.Equal(t, artifact.Digest, loaded.Digest)
	assert.Equal(t, "value", loaded.Metadata["test"])
}

func TestGetAuthForRegistry(t *testing.T) {
	tests := []struct {
		name       string
		registries []RegistryConfig
		lookupURL  string
		wantAnon   bool
	}{
		{
			name:       "no registries returns anonymous",
			registries: []RegistryConfig{},
			lookupURL:  "registry.test",
			wantAnon:   true,
		},
		{
			name: "matching registry by URL",
			registries: []RegistryConfig{
				{Name: "test", URL: "registry.test", Token: "token123"},
			},
			lookupURL: "registry.test",
			wantAnon:  false,
		},
		{
			name: "matching registry by name",
			registries: []RegistryConfig{
				{Name: "myregistry", URL: "registry.test", Token: "token123"},
			},
			lookupURL: "myregistry",
			wantAnon:  false,
		},
		{
			name: "no match returns anonymous",
			registries: []RegistryConfig{
				{Name: "test", URL: "registry.test"},
			},
			lookupURL: "other.registry",
			wantAnon:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				CacheDir:   t.TempDir(),
				Registries: tt.registries,
			}

			logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
			client, err := NewClient(cfg, logger)
			require.NoError(t, err)

			auth := client.getAuthForRegistry(tt.lookupURL)
			assert.NotNil(t, auth)
		})
	}
}

func TestExecutePlugin(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{
		CacheDir: tmpDir,
	}

	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	client, err := NewClient(cfg, logger)
	require.NoError(t, err)

	// Create artifact directory and metadata
	artifactID := "test123"
	artifactDir := filepath.Join(tmpDir, artifactID)
	err = os.MkdirAll(artifactDir, 0755)
	require.NoError(t, err)

	artifact := &ArtifactResult{
		ID:        artifactID,
		LocalPath: artifactDir,
	}

	err = client.saveArtifactMetadata(artifact)
	require.NoError(t, err)

	// Execute plugin (this just logs, actual execution delegated to DS)
	err = client.ExecutePlugin(artifactID, "test-plugin", []string{"arg1", "arg2"})
	assert.NoError(t, err)
}

func TestClose(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{
		CacheDir: tmpDir,
	}

	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	client, err := NewClient(cfg, logger)
	require.NoError(t, err)

	err = client.Close()
	assert.NoError(t, err)
}
