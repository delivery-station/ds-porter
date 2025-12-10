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
		switch os.Args[1] {
		case "-version", "--version":
			lines := []string{
				fmt.Sprintf("porter version %s", version),
				fmt.Sprintf("  commit: %s", commit),
				fmt.Sprintf("  built:  %s", date),
			}
			writeLines(os.Stdout, lines)
			return
		case "-help", "--help", "help":
			fmt.Fprintln(os.Stderr, "porter is a Delivery Station plugin and must be launched by DS.")
			os.Exit(1)
		}
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:       "porter",
		Output:     os.Stderr,
		Level:      hclog.Info,
		JSONFormat: true,
	})

	porterPlugin := NewPorterPlugin(logger, version, commit, date)

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: pkgplugin.Handshake,
		Plugins: map[string]plugin.Plugin{
			"ds-plugin": &pkgplugin.DSPlugin{Impl: porterPlugin},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}

func handlePull(client *porter.Client, args []string, logger hclog.Logger, stdout io.Writer) (*porter.ArtifactResult, error) {
	for _, arg := range args {
		if isHelpFlag(arg) {
			printPullUsage(stdout)
			return nil, nil
		}
	}

	if len(args) < 1 {
		printPullUsage(stdout)
		return nil, fmt.Errorf("artifact reference required")
	}

	var ref string
	var insecure bool
	var output string
	var allPlatforms bool
	var platformSelections []string

	// Parse args
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isHelpFlag(arg) {
			printPullUsage(stdout)
			return nil, nil
		} else if arg == "--insecure" {
			insecure = true
		} else if arg == "-o" || arg == "--output" {
			if i+1 < len(args) {
				output = args[i+1]
				i++ // skip next arg
			} else {
				return nil, fmt.Errorf("output path required for %s", arg)
			}
		} else if arg == "--all-arch" {
			allPlatforms = true
		} else if arg == "--platform" {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("platform value required for %s", arg)
			}
			platformSelections = append(platformSelections, args[i+1])
			i++
		} else if strings.HasPrefix(arg, "--platform=") {
			platformSelections = append(platformSelections, strings.TrimPrefix(arg, "--platform="))
		} else if strings.HasPrefix(arg, "--output=") {
			output = strings.TrimPrefix(arg, "--output=")
		} else if ref == "" {
			ref = arg
		}
	}

	if ref == "" {
		return nil, fmt.Errorf("artifact reference required")
	}

	if allPlatforms && len(platformSelections) > 0 {
		return nil, fmt.Errorf("--all-arch cannot be combined with --platform")
	}

	result, err := client.PullArtifact(ref, insecure)
	if err != nil {
		return nil, err
	}

	// If output is specified, export the artifact
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
		}
	}

	return result, nil
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

func isHelpFlag(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "-h", "--help", "help":
		return true
	default:
		return false
	}
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

func handlePush(client *porter.Client, args []string, logger hclog.Logger, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("artifact reference required")
	}

	// Check for manifest flag
	var manifestPath string
	var ref string
	var path string
	var insecure bool
	var positionalArgs []string

	// Parse args
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--manifest=") {
			manifestPath = arg[11:]
			continue
		}
		if arg == "--manifest" {
			if i+1 >= len(args) {
				return fmt.Errorf("manifest path required for --manifest")
			}
			manifestPath = args[i+1]
			i++
			continue
		}
		if arg == "--insecure" {
			insecure = true
			continue
		}

		positionalArgs = append(positionalArgs, arg)
	}

	// Multi-arch push via manifest
	if manifestPath != "" {
		if len(positionalArgs) < 1 {
			return fmt.Errorf("registry reference required")
		}
		ref = positionalArgs[0]
		return handleMultiArchPush(client, ref, manifestPath, logger, stdout, insecure)
	}

	// Single artifact push
	if len(positionalArgs) < 2 {
		return fmt.Errorf("artifact path and reference required")
	}
	path = positionalArgs[0]
	ref = positionalArgs[1]

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

func handleList(client *porter.Client, args []string, logger hclog.Logger, stdout io.Writer) error {
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

func handleExecutePlugin(client *porter.Client, args []string, logger hclog.Logger, stdout io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("artifact ID and plugin name required")
	}

	artifactID := args[0]
	pluginName := args[1]
	pluginArgs := args[2:]

	return client.ExecutePlugin(artifactID, pluginName, pluginArgs)
}
