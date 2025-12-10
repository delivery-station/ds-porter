package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/delivery-station/ds/pkg/types"
	"github.com/delivery-station/porter/pkg/porter"
	"github.com/hashicorp/go-hclog"
)

// PorterPlugin implements the DS PluginProtocol
type PorterPlugin struct {
	logger      hclog.Logger
	version     string
	commit      string
	date        string
	logCloser   io.Closer
	lastLogging porter.NormalizedLogging
}

func NewPorterPlugin(logger hclog.Logger, version, commit, date string) *PorterPlugin {
	return &PorterPlugin{
		logger:  logger,
		version: version,
		commit:  commit,
		date:    date,
	}
}

func (p *PorterPlugin) GetManifest(ctx context.Context) (*types.PluginInfo, error) {
	return &types.PluginInfo{
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

func (p *PorterPlugin) Execute(ctx context.Context, operation string, args []string) (*types.ExecutionResult, error) {
	// Load configuration supplied by DS host
	config, err := porter.LoadConfigFromHost(ctx)
	if err != nil {
		p.logger.Error("Failed to load configuration from DS", "error", err)
		return &types.ExecutionResult{
			ExitCode: 1,
			Error:    fmt.Sprintf("failed to load configuration from DS: %v", err),
		}, nil
	}

	normalizedLogging := porter.NormalizeLoggingConfig(config.Logging, config.LogLevel)
	if !normalizedLogging.LevelValid && strings.TrimSpace(config.Logging.Level) != "" {
		p.logger.Warn("Received unknown log level from DS", "level", config.Logging.Level)
	}
	if !normalizedLogging.FormatValid && strings.TrimSpace(config.Logging.Format) != "" {
		p.logger.Warn("Received unknown log format from DS", "format", config.Logging.Format)
	}

	outputTarget := logOutputTarget(normalizedLogging.Output)
	p.logger.Debug("Applying DS logging configuration", "level", normalizedLogging.Level, "format", normalizedLogging.Format, "output", outputTarget)

	if err := p.applyLoggingConfig(normalizedLogging); err != nil {
		p.logger.Warn("Failed to apply logging configuration", "error", err)
	}

	config.LogLevel = normalizedLogging.Level
	config.Logging.Level = normalizedLogging.Level
	config.Logging.Format = normalizedLogging.Format
	config.Logging.Output = normalizedLogging.Output

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
	p.logger.Debug("Executing porter operation", "operation", operation, "arg_count", len(args))

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

func (p *PorterPlugin) applyLoggingConfig(normalized porter.NormalizedLogging) error {
	if p.logger == nil {
		logger, closer, err := newLoggerForConfig(normalized)
		if err != nil {
			return err
		}
		p.logger = logger
		p.logCloser = closer
		p.lastLogging = normalized
		return nil
	}

	if p.lastLogging.Equal(normalized) {
		porter.ApplyLogLevel(p.logger, normalized)
		return nil
	}

	if p.lastLogging.Format == normalized.Format && p.lastLogging.Output == normalized.Output {
		porter.ApplyLogLevel(p.logger, normalized)
		p.lastLogging = normalized
		return nil
	}

	logger, closer, err := newLoggerForConfig(normalized)
	if err != nil {
		return err
	}

	if p.logCloser != nil {
		_ = p.logCloser.Close()
	}

	porter.ApplyLogLevel(logger, normalized)
	p.logger = logger
	p.logCloser = closer
	p.lastLogging = normalized
	return nil
}

func newLoggerForConfig(normalized porter.NormalizedLogging) (hclog.Logger, io.Closer, error) {
	writer, closer, err := resolveLogOutput(normalized.Output)
	if err != nil {
		return nil, nil, err
	}

	lvl := hclog.LevelFromString(normalized.Level)
	if lvl == hclog.NoLevel {
		lvl = hclog.Info
	}

	opts := &hclog.LoggerOptions{
		Name:       "porter",
		Output:     writer,
		Level:      lvl,
		JSONFormat: normalized.IsJSON(),
		Color:      hclog.AutoColor,
	}
	if normalized.IsJSON() {
		opts.Color = hclog.ColorOff
	}

	return hclog.New(opts), closer, nil
}

func resolveLogOutput(output string) (io.Writer, io.Closer, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return os.Stderr, nil, nil
	}
	if strings.EqualFold(trimmed, "stdout") {
		return os.Stdout, nil, nil
	}
	if strings.EqualFold(trimmed, "stderr") {
		return os.Stderr, nil, nil
	}

	dir := filepath.Dir(trimmed)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
	}

	file, err := os.OpenFile(trimmed, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}

	return file, file, nil
}

func (p *PorterPlugin) Close() error {
	if p.logCloser == nil {
		return nil
	}

	defer func() {
		p.logCloser = nil
	}()

	return p.logCloser.Close()
}

func logOutputTarget(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "stderr"
	}
	return trimmed
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
