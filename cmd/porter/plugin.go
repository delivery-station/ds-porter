package main

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/delivery-station/ds/pkg/types"
	"github.com/delivery-station/porter/pkg/porter"
	"github.com/hashicorp/go-hclog"
)

// PorterPlugin implements the DS PluginProtocol
type PorterPlugin struct {
	logger  hclog.Logger
	version string
	commit  string
	date    string
}

func NewPorterPlugin(logger hclog.Logger, version, commit, date string) *PorterPlugin {
	return &PorterPlugin{
		logger:  logger,
		version: version,
		commit:  commit,
		date:    date,
	}
}

func (p *PorterPlugin) GetMetadata(ctx context.Context) (*types.PluginMetadata, error) {
	return &types.PluginMetadata{
		Name:        "porter",
		Version:     p.version,
		Description: "Fetch and deliver OCI artifacts",
		Operations:  []string{"pull", "push", "list", "execute-plugin"},
		Platform: types.PluginPlatform{
			OS:   []string{"linux", "darwin", "windows"},
			Arch: []string{"amd64", "arm64"},
		},
	}, nil
}

func (p *PorterPlugin) Execute(ctx context.Context, operation string, args []string, env map[string]string) (*types.ExecutionResult, error) {
	// Set env vars for the operation
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			return &types.ExecutionResult{
				ExitCode: 1,
				Error:    fmt.Sprintf("failed to set environment variable %s: %v", k, err),
			}, nil
		}
	}

	// Let's create a client
	config, err := porter.LoadConfigFromEnv() // This reads os.Environ()
	if err != nil {
		return &types.ExecutionResult{
			ExitCode: 1,
			Error:    fmt.Sprintf("Failed to load configuration: %v", err),
		}, nil
	}

	client, err := porter.NewClient(config, p.logger)
	if err != nil {
		return &types.ExecutionResult{
			ExitCode: 1,
			Error:    fmt.Sprintf("Failed to create porter client: %v", err),
		}, nil
	}
	defer func() {
		if err := client.Close(); err != nil {
			p.logger.Warn("Failed to close porter client", "error", err)
		}
	}()

	// Capture stdout
	var stdoutBuf bytes.Buffer
	var errExec error

	switch operation {
	case "pull":
		errExec = handlePull(client, args, p.logger, &stdoutBuf)
	case "push":
		errExec = handlePush(client, args, p.logger, &stdoutBuf)
	case "list":
		errExec = handleList(client, args, p.logger, &stdoutBuf)
	case "execute-plugin":
		errExec = handleExecutePlugin(client, args, p.logger, &stdoutBuf)
	case "help":
		stdoutBuf.WriteString(`Available commands:
  pull <artifact>    Pull an artifact
  push <artifact>    Push an artifact
  list               List artifacts
  execute-plugin     Execute a plugin
  version            Show plugin version
`)
	case "version":
		stdoutBuf.WriteString(fmt.Sprintf("porter version %s\n  commit: %s\n  built:  %s", p.version, p.commit, p.date))
	default:
		errExec = fmt.Errorf("unknown operation: %s", operation)
	}

	if errExec != nil {
		return &types.ExecutionResult{
			ExitCode: 1,
			Error:    errExec.Error(),
		}, nil
	}

	return &types.ExecutionResult{
		Stdout:   stdoutBuf.String(),
		ExitCode: 0,
	}, nil
}

func (p *PorterPlugin) ValidateConfig(ctx context.Context, config map[string]interface{}) error {
	return nil
}

func (p *PorterPlugin) GetSchema(ctx context.Context) (*types.PluginSchema, error) {
	return &types.PluginSchema{
		Version: "1.0.0",
		Properties: map[string]types.SchemaProperty{
			"registry": {
				Type:        "string",
				Description: "Default registry",
				Required:    false,
				Default:     "ghcr.io",
			},
		},
	}, nil
}
