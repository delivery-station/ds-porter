package adapter

import (
	"context"
	"testing"
	"time"

	"github.com/delivery-station/ds/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPorterAdapter(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &types.Config{
		Registry: types.RegistryConfig{
			Default: "ghcr.io",
		},
		Cache: types.CacheConfig{
			Dir:     tmpDir + "/cache",
			MaxSize: 1 * 1024 * 1024 * 1024, // 1GB in bytes
			TTL:     1 * time.Hour,
		},
		Plugins: types.PluginsConfig{
			Dir: tmpDir + "/plugins",
		},
	}

	adapter, err := NewPorterAdapter(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, adapter)
	defer adapter.Close()

	t.Run("StoreAndRetrieveInstallationState", func(t *testing.T) {
		ctx := context.Background()
		installationID := "test-installation"
		state := map[string]interface{}{
			"status": "installed",
			"bundle": "test-bundle:v1.0.0",
		}

		ttl := 1 * time.Hour
		err := adapter.StoreInstallationState(ctx, installationID, state, &ttl)
		require.NoError(t, err)

		retrieved, err := adapter.GetInstallationState(ctx, installationID)
		require.NoError(t, err)
		assert.Equal(t, "installed", retrieved["status"])
		assert.Equal(t, "test-bundle:v1.0.0", retrieved["bundle"])
	})

	t.Run("StoreAndRetrieveBundleMetadata", func(t *testing.T) {
		ctx := context.Background()
		bundleRef := "test-bundle:v1.0.0"
		metadata := map[string]interface{}{
			"name":    "test-bundle",
			"version": "v1.0.0",
		}

		ttl := 1 * time.Hour
		err := adapter.StoreBundleMetadata(ctx, bundleRef, metadata, &ttl)
		require.NoError(t, err)

		retrieved, err := adapter.GetBundleMetadata(ctx, bundleRef)
		require.NoError(t, err)
		assert.Equal(t, "test-bundle", retrieved["name"])
		assert.Equal(t, "v1.0.0", retrieved["version"])
	})
}

func TestPorterAdapterClient(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &types.Config{
		Registry: types.RegistryConfig{
			Default: "ghcr.io",
		},
		Cache: types.CacheConfig{
			Dir:     tmpDir + "/cache",
			MaxSize: 1 * 1024 * 1024 * 1024, // 1GB in bytes
			TTL:     1 * time.Hour,
		},
		Plugins: types.PluginsConfig{
			Dir: tmpDir + "/plugins",
		},
	}

	adapter, err := NewPorterAdapter(cfg, nil)
	require.NoError(t, err)
	defer adapter.Close()

	client := adapter.Client()
	assert.NotNil(t, client)
}
