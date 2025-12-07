package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallationStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewInstallationStore(tmpDir, nil)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("SaveAndGetInstallation", func(t *testing.T) {
		installation := &Installation{
			ID:        "test-id",
			Namespace: "default",
			Name:      "myapp",
			Bundle:    "ghcr.io/myorg/myapp:v1.0.0",
			Status:    "installed",
			Created:   time.Now(),
			Parameters: map[string]interface{}{
				"port": 8080,
			},
		}

		err := store.Save(ctx, installation)
		require.NoError(t, err)

		retrieved, err := store.Get(ctx, "default", "myapp")
		require.NoError(t, err)
		assert.Equal(t, "test-id", retrieved.ID)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, "myapp", retrieved.Name)
		assert.Equal(t, "installed", retrieved.Status)
		assert.Equal(t, 8080, int(retrieved.Parameters["port"].(float64)))
	})

	t.Run("ListInstallations", func(t *testing.T) {
		// Save multiple installations
		for i := 1; i <= 3; i++ {
			installation := &Installation{
				ID:        string(rune('a' + i)),
				Namespace: "test-namespace",
				Name:      string(rune('a' + i)),
				Bundle:    "test-bundle",
				Status:    "installed",
				Created:   time.Now(),
			}
			err := store.Save(ctx, installation)
			require.NoError(t, err)
		}

		installations, err := store.List(ctx, "test-namespace")
		require.NoError(t, err)
		assert.Len(t, installations, 3)
	})

	t.Run("DeleteInstallation", func(t *testing.T) {
		installation := &Installation{
			ID:        "delete-test",
			Namespace: "delete-ns",
			Name:      "delete-app",
			Bundle:    "test-bundle",
			Status:    "installed",
			Created:   time.Now(),
		}

		err := store.Save(ctx, installation)
		require.NoError(t, err)

		err = store.Delete(ctx, "delete-ns", "delete-app")
		require.NoError(t, err)

		_, err = store.Get(ctx, "delete-ns", "delete-app")
		assert.Error(t, err)
	})

	t.Run("GetNonExistentInstallation", func(t *testing.T) {
		_, err := store.Get(ctx, "nonexistent", "nonexistent")
		assert.Error(t, err)
	})

	t.Run("DeleteNonExistentInstallation", func(t *testing.T) {
		err := store.Delete(ctx, "nonexistent", "nonexistent")
		assert.Error(t, err)
	})

	t.Run("ListEmptyNamespace", func(t *testing.T) {
		installations, err := store.List(ctx, "empty-namespace")
		require.NoError(t, err)
		assert.Empty(t, installations)
	})
}
