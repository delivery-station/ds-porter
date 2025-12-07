package main

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
)

func TestPorterPlugin_Execute_Help(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	plugin := NewPorterPlugin(logger, "0.1.0", "test-commit", "test-date")

	ctx := context.Background()
	env := make(map[string]string)

	result, err := plugin.Execute(ctx, "help", []string{}, env)
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
}

func TestPorterPlugin_Execute_Unknown(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	plugin := NewPorterPlugin(logger, "0.1.0", "test-commit", "test-date")

	ctx := context.Background()
	env := make(map[string]string)

	result, err := plugin.Execute(ctx, "unknown", []string{}, env)
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
}

func TestPorterPlugin_Execute_Version(t *testing.T) {
	logger := hclog.New(&hclog.LoggerOptions{Name: "test", Level: hclog.Debug})
	plugin := NewPorterPlugin(logger, "0.1.0", "test-commit", "test-date")

	ctx := context.Background()
	env := make(map[string]string)

	result, err := plugin.Execute(ctx, "version", []string{}, env)
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
}
