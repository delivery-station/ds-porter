package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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

func (p *PorterPlugin) GetManifest(ctx context.Context) (*types.PluginManifest, error) {
	return &types.PluginManifest{
		Name:        "porter",
		Version:     p.version,
		Description: "Fetch and deliver OCI artifacts",
		Commands: []types.PluginCommand{
			{Name: "pull", Description: "Pull an OCI artifact"},
			{Name: "push", Description: "Push an OCI artifact"},
			{Name: "list", Description: "List cached artifacts"},
			{Name: "execute-plugin", Description: "Execute a plugin contained in an artifact"},
			{Name: "version", Description: "Display plugin version information"},
		},
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

	// Load configuration supplied by DS host
	config, err := porter.LoadConfigFromHost(ctx)
	if err != nil {
		p.logger.Error("Failed to load configuration from DS", "error", err)
		return &types.ExecutionResult{
			ExitCode: 1,
			Error:    fmt.Sprintf("failed to load configuration from DS: %v", err),
		}, nil
	}

	if level := strings.TrimSpace(config.LogLevel); level != "" {
		if parsed := hclog.LevelFromString(level); parsed != hclog.NoLevel {
			p.logger.SetLevel(parsed)
		} else {
			p.logger.Warn("Received unknown log level from DS", "level", level)
		}
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
	finalizers := []types.FinalizerRequest{}
	parsedArgs := types.NewPluginArgs(args)

	switch operation {
	case "pull":
		var pullResult *porter.ArtifactResult
		pullResult, errExec = handlePull(client, parsedArgs, p.logger, &stdoutBuf)
		if errExec == nil && pullResult != nil {
			jsonOutput, marshalErr := json.Marshal(pullResult)
			if marshalErr != nil {
				errExec = fmt.Errorf("failed to marshal result: %w", marshalErr)
			} else {
				stdoutBuf.Write(jsonOutput)
				stdoutBuf.WriteByte('\n')
				finalizers = append(finalizers, finalizersFromMetadata(pullResult.Metadata)...)
			}
		}
	case "push":
		errExec = handlePush(client, parsedArgs, p.logger, &stdoutBuf)
	case "list":
		errExec = handleList(client, parsedArgs, p.logger, &stdoutBuf)
	case "execute-plugin":
		errExec = handleExecutePlugin(client, parsedArgs, p.logger, &stdoutBuf)
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
		Stdout:     stdoutBuf.String(),
		ExitCode:   0,
		Finalizers: finalizers,
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

func finalizersFromMetadata(metadata map[string]string) []types.FinalizerRequest {
	if len(metadata) == 0 {
		return nil
	}

	name := firstNonEmpty(metadata, "ds.finalizer", "finalizer")
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}

	operation := strings.TrimSpace(firstNonEmpty(metadata, "ds.finalizer.operation", "finalizer.operation"))
	if operation == "" {
		operation = "upload"
	}

	rawArgs := strings.TrimSpace(firstNonEmpty(metadata, "ds.finalizer.args", "finalizer.args"))
	args := parseFinalizerArgs(rawArgs)

	return []types.FinalizerRequest{{
		Name:      name,
		Operation: operation,
		Args:      args,
	}}
}

func firstNonEmpty(metadata map[string]string, keys ...string) string {
	for _, k := range keys {
		if val, ok := metadata[k]; ok {
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func parseFinalizerArgs(raw string) []string {
	if raw == "" {
		return nil
	}

	if strings.HasPrefix(raw, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			cleaned := make([]string, 0, len(arr))
			for _, v := range arr {
				if trimmed := strings.TrimSpace(v); trimmed != "" {
					cleaned = append(cleaned, trimmed)
				}
			}
			if len(cleaned) > 0 {
				return cleaned
			}
			return nil
		}
	}

	parts := strings.Split(raw, ",")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}
