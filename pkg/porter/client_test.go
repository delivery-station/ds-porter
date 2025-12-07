package porter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		wantErr  bool
	}{
		{
			name:     "empty config uses defaults",
			envValue: "",
			wantErr:  false,
		},
		{
			name: "valid JSON config",
			envValue: `{
"registries": [{"name": "test", "url": "registry.test"}],
"cache_dir": "/tmp/test-cache"
}`,
			wantErr: false,
		},
		{
			name:     "invalid JSON",
			envValue: "{invalid json",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("DS_PORTER_CONFIG", tt.envValue)
			defer os.Unsetenv("DS_PORTER_CONFIG")

			cfg, err := LoadConfigFromEnv()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, cfg)
			}
		})
	}
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
