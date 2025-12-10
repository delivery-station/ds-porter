package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/delivery-station/porter/pkg/porter"
	"github.com/delivery-station/porter/pkg/release"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/hashicorp/go-hclog"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	pkgplugin "github.com/delivery-station/ds/pkg/plugin"
	"github.com/delivery-station/ds/pkg/types"
	"github.com/hashicorp/go-plugin"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// DS Plugin Entry Point
// This binary is loaded by DS as a plugin named "porter"
// It receives commands via: ds porter <operation> [args]

func main() {
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "porter is a Delivery Station plugin and must be launched by DS.")
		os.Exit(1)
	}

	logger := hclog.New(&hclog.LoggerOptions{Name: "porter"})

	porterPlugin := NewPorterPlugin(logger, version, commit, date)

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: pkgplugin.Handshake,
		Plugins: map[string]plugin.Plugin{
			"ds-plugin": &pkgplugin.DSPlugin{Impl: porterPlugin},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}

func handlePull(client *porter.Client, args types.PluginArgs, logger hclog.Logger, stdout io.Writer) (*porter.ArtifactResult, error) {
	if help, ok := args.BoolAny("help", "h"); ok && help {
		printPullUsage(stdout)
		return nil, nil
	}

	ref, _ := args.FirstAny("ref", "artifact", "arg0")
	ref = strings.TrimSpace(ref)
	if ref == "" {
		printPullUsage(stdout)
		return nil, fmt.Errorf("artifact reference required")
	}

	insecure := false
	if val, ok := args.Bool("insecure"); ok {
		insecure = val
	}

	output, _ := args.FirstAny("output", "o")
	output = strings.TrimSpace(output)

	allPlatforms := false
	if val, ok := args.BoolAny("all-arch"); ok {
		allPlatforms = val
	}

	platformSelections := cleanedValues(args.All("platform"))
	platformSelections = append(platformSelections, cleanedValues(args.All("platforms"))...)

	if allPlatforms && len(platformSelections) > 0 {
		return nil, fmt.Errorf("--all-arch cannot be combined with --platform")
	}
	logger.Debug("Resolved pull options", "ref", ref, "insecure", insecure, "output", output, "all_platforms", allPlatforms, "platforms", platformSelections)

	result, err := client.PullArtifact(ref, insecure)
	if err != nil {
		return nil, err
	}
	if result != nil {
		logger.Debug("Pull completed", "ref", ref, "digest", result.Digest, "cached", result.Cached, "cache_path", result.LocalPath)
	}

	if output != "" {
		exportOpts, err := buildExportOptions(allPlatforms, platformSelections)
		if err != nil {
			return nil, err
		}

		exportedPaths, err := client.ExportArtifact(result, output, exportOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to export artifact: %w", err)
		}
		result.ExportedFiles = exportedPaths
		for _, p := range exportedPaths {
			logger.Info("Artifact exported", "path", p)
		}

		if len(exportedPaths) > 0 {
			if finalizerName := firstNonEmpty(result.Metadata, "ds.finalizer", "finalizer"); strings.TrimSpace(finalizerName) != "" {
				if _, ok := result.Metadata["ds.finalizer.args"]; !ok {
					resolved := output
					if abs, err := filepath.Abs(output); err == nil {
						resolved = abs
					} else {
						logger.Warn("Failed to resolve absolute path for finalizer", "path", output, "error", err)
					}

					if _, err := os.Stat(resolved); err != nil {
						logger.Warn("Finalizer path does not exist", "path", resolved, "error", err)
					}

					argsPayload := []string{resolved}
					encoded, err := json.Marshal(argsPayload)
					if err != nil {
						logger.Warn("Failed to encode finalizer arguments", "path", resolved, "error", err)
						result.Metadata["ds.finalizer.args"] = resolved
					} else {
						result.Metadata["ds.finalizer.args"] = string(encoded)
					}
				}
			}
			logger.Debug("Exported artifact content", "paths", exportedPaths)
		}
	}

	return result, nil
}

func cleanedValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	out := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func buildExportOptions(allPlatforms bool, selections []string) (porter.ExportOptions, error) {
	if allPlatforms {
		return porter.ExportOptions{AllPlatforms: true, UsePlatformSubdirs: true}, nil
	}

	if len(selections) == 0 {
		return porter.ExportOptions{
			Platforms: []ocispec.Platform{{
				OS:           runtime.GOOS,
				Architecture: runtime.GOARCH,
			}},
			UsePlatformSubdirs: false,
		}, nil
	}

	opts := porter.ExportOptions{Platforms: make([]ocispec.Platform, 0, len(selections)), UsePlatformSubdirs: true}
	for _, sel := range selections {
		plat, err := parsePlatformSelection(sel)
		if err != nil {
			return porter.ExportOptions{}, err
		}
		opts.Platforms = append(opts.Platforms, plat)
	}

	return opts, nil
}

func parsePlatformSelection(value string) (ocispec.Platform, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ocispec.Platform{}, fmt.Errorf("platform cannot be empty")
	}

	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return ocispec.Platform{}, fmt.Errorf("invalid platform %q, expected os/arch or os/arch/variant", value)
	}

	plat := ocispec.Platform{
		OS:           strings.ToLower(parts[0]),
		Architecture: strings.ToLower(parts[1]),
	}

	if len(parts) > 2 {
		plat.Variant = strings.ToLower(strings.Join(parts[2:], "/"))
	}

	return plat, nil
}

func printPullUsage(w io.Writer) {
	lines := []string{
		"Usage: ds porter pull [flags] <artifact-ref>",
		"",
		"Flags:",
		"  --output, -o <path>   Export artifact to a file or directory",
		"                         Directories receive ds-porter by default; files write the binary directly",
		"  --platform <os/arch>  Fetch a specific platform (repeatable; e.g. linux/arm64)",
		"  --all-arch            Fetch every platform in the index (requires directory output)",
		"  --insecure            Allow plain HTTP connections to registries",
		"",
		"Behaviour:",
		"  • Without --platform/--all-arch, the current runtime platform is exported",
		"  • When multiple platforms are requested, artifacts are written to <dir>/<os>/<arch>/",
		"",
		"Examples:",
		"  ds porter pull ghcr.io/delivery-station/porter:0.2.0 -o ./porter-bin",
		"  ds porter pull localhost/delivery-station/porter:0.2.0 --platform linux/arm64 -o ./out",
		"  ds porter pull ghcr.io/...:0.2.0 --all-arch -o ./artifacts",
	}
	writeLines(w, lines)
}

func handlePush(client *porter.Client, args types.PluginArgs, logger hclog.Logger, stdout io.Writer) error {
	manifestPath, _ := args.FirstAny("manifest", "m")
	manifestPath = strings.TrimSpace(manifestPath)

	insecure := false
	if val, ok := args.Bool("insecure"); ok {
		insecure = val
	}

	positionals := cleanedValues(args.Positionals())

	if manifestPath != "" {
		if len(positionals) < 1 {
			return fmt.Errorf("registry reference required")
		}
		ref := positionals[0]
		return handleMultiArchPush(client, ref, manifestPath, logger, stdout, insecure)
	}

	if len(positionals) < 2 {
		return fmt.Errorf("artifact path and reference required")
	}

	path := positionals[0]
	ref := positionals[1]

	result, err := client.PushArtifact(path, ref, insecure)
	if err != nil {
		return err
	}

	output, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal push result: %w", err)
	}
	if _, err := fmt.Fprintln(stdout, string(output)); err != nil {
		return fmt.Errorf("failed to write push result: %w", err)
	}
	return nil
}

func writeLines(w io.Writer, lines []string) {
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			_, _ = fmt.Fprintf(w, "error writing output: %v\n", err)
			return
		}
	}
}

func handleMultiArchPush(client *porter.Client, ref, manifestPath string, logger hclog.Logger, stdout io.Writer, insecure bool) error {
	// Parse registry and repository from ref
	// ref format: registry/repo[:tag]
	// We need to split this for ReleaseConfig
	// Actually, let's just pass the full ref and let the pusher handle it

	parsedRef, err := name.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("invalid reference %q: %w", ref, err)
	}

	username, password := client.ResolveCredentials(parsedRef.Context().RegistryStr())

	// Config
	config := release.ReleaseConfig{
		Reference:    ref,
		Username:     username,
		Password:     password,
		ManifestPath: manifestPath,
		TagLatest:    true, // Default to true
		Insecure:     insecure,
	}

	pusher, err := release.NewPusher(config)
	if err != nil {
		return fmt.Errorf("failed to create pusher: %w", err)
	}

	return pusher.Push(context.Background(), stdout)
}

func handleList(client *porter.Client, _ types.PluginArgs, logger hclog.Logger, stdout io.Writer) error {
	artifacts, err := client.ListCachedArtifacts()
	if err != nil {
		return err
	}

	output, err := json.Marshal(artifacts)
	if err != nil {
		return fmt.Errorf("failed to marshal artifact list: %w", err)
	}
	if _, err := fmt.Fprintln(stdout, string(output)); err != nil {
		return fmt.Errorf("failed to write artifact list: %w", err)
	}
	return nil
}

func handleExecutePlugin(client *porter.Client, args types.PluginArgs, logger hclog.Logger, stdout io.Writer) error {
	positionals := cleanedValues(args.Positionals())
	if len(positionals) < 2 {
		return fmt.Errorf("artifact ID and plugin name required")
	}

	artifactID := positionals[0]
	pluginName := positionals[1]
	pluginArgs := positionals[2:]

	return client.ExecutePlugin(artifactID, pluginName, pluginArgs)
}
