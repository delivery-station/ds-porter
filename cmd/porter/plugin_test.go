package main

import (
	"context"
	"strings"
	"testing"

	"github.com/delivery-station/ds/pkg/types"
	"github.com/hashicorp/go-hclog"
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

func newHostConfigContext(t *testing.T) context.Context {
	t.Helper()
	provider := &stubHostConfigProvider{
		cfg: &types.Config{
			Cache:    types.CacheConfig{Dir: t.TempDir()},
			Registry: types.RegistryConfig{Default: "ghcr.io/delivery-station"},
			Logging:  types.LoggingConfig{Level: "debug"},
		},
	}
	return types.WithHostConfigProvider(context.Background(), provider)
}

func TestPorterPlugin_Execute_Help(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	plugin := NewPorterPlugin(logger, "0.1.0", "test-commit", "test-date")

	ctx := newHostConfigContext(t)

	result, err := plugin.Execute(ctx, "help", []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}

	expectedOutput := "Available commands:"
	if !strings.Contains(result.Stdout, expectedOutput) {
		t.Errorf("expected output to contain %q, got %q", expectedOutput, result.Stdout)
	}

	if plugin.logger.GetLevel() != hclog.Debug {
		t.Fatalf("expected logger level debug, got %s", plugin.logger.GetLevel())
	}
}

func TestPorterPlugin_GetManifest(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Info})
	plug := NewPorterPlugin(logger, "0.9.1", "abcdef123", "2025-12-01T00:00:00Z")

	manifest, err := plug.GetManifest(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if manifest == nil {
		t.Fatalf("expected manifest, got nil")
	}

	if manifest.Name != "porter" {
		t.Fatalf("expected name 'porter', got %q", manifest.Name)
	}

	if manifest.Version != "0.9.1" {
		t.Fatalf("expected version '0.9.1', got %q", manifest.Version)
	}

	if manifest.Description == "" {
		t.Fatalf("expected description to be populated")
	}

	if len(manifest.Commands) < 4 {
		t.Fatalf("expected commands to include primary operations, got %v", manifest.Commands)
	}

	if len(manifest.Platform.OS) == 0 || len(manifest.Platform.Arch) == 0 {
		t.Fatalf("expected platform compatibility to be defined")
	}
}

func TestPorterPlugin_Execute_Unknown(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	plugin := NewPorterPlugin(logger, "0.1.0", "test-commit", "test-date")

	ctx := newHostConfigContext(t)

	result, err := plugin.Execute(ctx, "unknown", []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}

	expectedError := "unknown operation: unknown"
	if result.Error != expectedError {
		t.Errorf("expected error %q, got %q", expectedError, result.Error)
	}

	if plugin.logger.GetLevel() != hclog.Debug {
		t.Fatalf("expected logger level debug, got %s", plugin.logger.GetLevel())
	}
}

func TestPorterPlugin_Execute_Version(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	plugin := NewPorterPlugin(logger, "0.1.0", "test-commit", "test-date")

	ctx := newHostConfigContext(t)

	result, err := plugin.Execute(ctx, "version", []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}

	expectedOutput := "porter version 0.1.0\n  commit: test-commit\n  built:  test-date"
	if !strings.Contains(result.Stdout, expectedOutput) {
		t.Errorf("expected output to contain %q, got %q", expectedOutput, result.Stdout)
	}

	if plugin.logger.GetLevel() != hclog.Debug {
		t.Fatalf("expected logger level debug, got %s", plugin.logger.GetLevel())
	}
}
