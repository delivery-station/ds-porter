package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
)

// Installation represents a Porter installation
type Installation struct {
	ID          string                 `json:"id"`
	Namespace   string                 `json:"namespace"`
	Name        string                 `json:"name"`
	Bundle      string                 `json:"bundle"`
	Status      string                 `json:"status"`
	Created     time.Time              `json:"created"`
	Modified    time.Time              `json:"modified"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Credentials map[string]interface{} `json:"credentials,omitempty"`
	Outputs     map[string]interface{} `json:"outputs,omitempty"`
}

// InstallationStore manages Porter installations
type InstallationStore struct {
	storePath string
	logger    hclog.Logger
	mu        sync.RWMutex
}

// NewInstallationStore creates a new installation store
func NewInstallationStore(storePath string, logger hclog.Logger) (*InstallationStore, error) {
	if logger == nil {
		logger = hclog.New(&hclog.LoggerOptions{
			Name:  "installation-store",
			Level: hclog.Info,
		})
	}

	if err := os.MkdirAll(storePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %w", err)
	}

	return &InstallationStore{
		storePath: storePath,
		logger:    logger,
	}, nil
}

// Save saves an installation
func (s *InstallationStore) Save(ctx context.Context, installation *Installation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	installation.Modified = time.Now()

	data, err := json.MarshalIndent(installation, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal installation: %w", err)
	}

	filePath := s.getFilePath(installation.Namespace, installation.Name)
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return fmt.Errorf("failed to create namespace directory: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write installation: %w", err)
	}

	s.logger.Info("Installation saved",
		"namespace", installation.Namespace,
		"name", installation.Name,
	)

	return nil
}

// Get retrieves an installation
func (s *InstallationStore) Get(ctx context.Context, namespace, name string) (*Installation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath := s.getFilePath(namespace, name)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("installation not found: %s/%s", namespace, name)
		}
		return nil, fmt.Errorf("failed to read installation: %w", err)
	}

	var installation Installation
	if err := json.Unmarshal(data, &installation); err != nil {
		return nil, fmt.Errorf("failed to unmarshal installation: %w", err)
	}

	return &installation, nil
}

// List lists all installations in a namespace
func (s *InstallationStore) List(ctx context.Context, namespace string) ([]*Installation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	namespacePath := filepath.Join(s.storePath, namespace)
	entries, err := os.ReadDir(namespacePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Installation{}, nil
		}
		return nil, fmt.Errorf("failed to read namespace directory: %w", err)
	}

	installations := make([]*Installation, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(namespacePath, entry.Name()))
		if err != nil {
			s.logger.Warn("Failed to read installation file", "file", entry.Name(), "error", err)
			continue
		}

		var installation Installation
		if err := json.Unmarshal(data, &installation); err != nil {
			s.logger.Warn("Failed to unmarshal installation", "file", entry.Name(), "error", err)
			continue
		}

		installations = append(installations, &installation)
	}

	return installations, nil
}

// Delete deletes an installation
func (s *InstallationStore) Delete(ctx context.Context, namespace, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := s.getFilePath(namespace, name)
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("installation not found: %s/%s", namespace, name)
		}
		return fmt.Errorf("failed to delete installation: %w", err)
	}

	s.logger.Info("Installation deleted",
		"namespace", namespace,
		"name", name,
	)

	return nil
}

// getFilePath returns the file path for an installation
func (s *InstallationStore) getFilePath(namespace, name string) string {
	return filepath.Join(s.storePath, namespace, fmt.Sprintf("%s.json", name))
}
