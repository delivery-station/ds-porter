package adapter

import (
	"context"
	"fmt"
	"io"
	"time"

	dsclient "github.com/delivery-station/ds/pkg/client"
	"github.com/delivery-station/ds/pkg/types"
	"github.com/hashicorp/go-hclog"
)

// PorterAdapter adapts the DS client for Porter-specific operations
type PorterAdapter struct {
	dsClient *dsclient.Client
	logger   hclog.Logger
}

// NewPorterAdapter creates a new Porter adapter
func NewPorterAdapter(cfg *types.Config, logger hclog.Logger) (*PorterAdapter, error) {
	if logger == nil {
		logger = hclog.New(&hclog.LoggerOptions{
			Name:  "porter-adapter",
			Level: hclog.Info,
		})
	}

	dsClient, err := dsclient.NewClient(
		dsclient.WithConfig(cfg),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create DS client: %w", err)
	}

	return &PorterAdapter{
		dsClient: dsClient,
		logger:   logger,
	}, nil
}

// PullInstallation pulls a Porter installation from a registry
func (a *PorterAdapter) PullInstallation(ctx context.Context, ref string, writer io.Writer) error {
	a.logger.Info("Pulling Porter installation", "ref", ref)

	if err := a.dsClient.Pull(ctx, ref, writer); err != nil {
		return fmt.Errorf("failed to pull installation %s: %w", ref, err)
	}

	// Publish event
	_ = a.dsClient.Publish(ctx, "installation.pulled", "porter", map[string]interface{}{
		"ref":       ref,
		"timestamp": time.Now().Unix(),
	})

	return nil
}

// PushInstallation pushes a Porter installation to a registry
func (a *PorterAdapter) PushInstallation(ctx context.Context, ref string, reader io.Reader, mediaType string) error {
	a.logger.Info("Pushing Porter installation", "ref", ref)

	if err := a.dsClient.Push(ctx, ref, reader, mediaType); err != nil {
		return fmt.Errorf("failed to push installation %s: %w", ref, err)
	}

	// Publish event
	_ = a.dsClient.Publish(ctx, "installation.pushed", "porter", map[string]interface{}{
		"ref":       ref,
		"timestamp": time.Now().Unix(),
	})

	return nil
}

// ListInstallations lists available Porter installations
func (a *PorterAdapter) ListInstallations(ctx context.Context, repository string) ([]string, error) {
	a.logger.Info("Listing Porter installations", "repository", repository)
	return a.dsClient.List(ctx, repository)
}

// PullBundle pulls a Porter bundle from a registry
func (a *PorterAdapter) PullBundle(ctx context.Context, ref string, writer io.Writer) error {
	a.logger.Info("Pulling Porter bundle", "ref", ref)

	if err := a.dsClient.Pull(ctx, ref, writer); err != nil {
		return fmt.Errorf("failed to pull bundle %s: %w", ref, err)
	}

	// Publish event
	_ = a.dsClient.Publish(ctx, "bundle.pulled", "porter", map[string]interface{}{
		"ref":       ref,
		"timestamp": time.Now().Unix(),
	})

	return nil
}

// PushBundle pushes a Porter bundle to a registry
func (a *PorterAdapter) PushBundle(ctx context.Context, ref string, reader io.Reader, mediaType string) error {
	a.logger.Info("Pushing Porter bundle", "ref", ref)

	if err := a.dsClient.Push(ctx, ref, reader, mediaType); err != nil {
		return fmt.Errorf("failed to push bundle %s: %w", ref, err)
	}

	// Publish event
	_ = a.dsClient.Publish(ctx, "bundle.pushed", "porter", map[string]interface{}{
		"ref":       ref,
		"timestamp": time.Now().Unix(),
	})

	return nil
}

// StoreInstallationState stores Porter installation state
func (a *PorterAdapter) StoreInstallationState(ctx context.Context, installationID string, state map[string]interface{}, ttl *time.Duration) error {
	key := fmt.Sprintf("installation:%s", installationID)
	return a.dsClient.SetState(ctx, key, "porter", state, ttl)
}

// GetInstallationState retrieves Porter installation state
func (a *PorterAdapter) GetInstallationState(ctx context.Context, installationID string) (map[string]interface{}, error) {
	key := fmt.Sprintf("installation:%s", installationID)
	return a.dsClient.GetState(ctx, key)
}

// StoreBundleMetadata stores Porter bundle metadata
func (a *PorterAdapter) StoreBundleMetadata(ctx context.Context, bundleRef string, metadata map[string]interface{}, ttl *time.Duration) error {
	key := fmt.Sprintf("bundle:%s", bundleRef)
	return a.dsClient.SetState(ctx, key, "porter", metadata, ttl)
}

// GetBundleMetadata retrieves Porter bundle metadata
func (a *PorterAdapter) GetBundleMetadata(ctx context.Context, bundleRef string) (map[string]interface{}, error) {
	key := fmt.Sprintf("bundle:%s", bundleRef)
	return a.dsClient.GetState(ctx, key)
}

// Close cleans up resources
func (a *PorterAdapter) Close() error {
	return a.dsClient.Close()
}

// Client returns the underlying DS client
func (a *PorterAdapter) Client() *dsclient.Client {
	return a.dsClient
}
