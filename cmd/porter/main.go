package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/delivery-station/porter/pkg/porter"
	"github.com/delivery-station/porter/pkg/release"
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
	logger := hclog.New(&hclog.LoggerOptions{
		Name:       "porter",
		Output:     os.Stderr,
		Level:      hclog.Info,
		JSONFormat: true,
	})

	// Check if we are running in plugin mode (no args)
	if len(os.Args) == 1 {
		porterPlugin := NewPorterPlugin(logger, version, commit, date)

		plugin.Serve(&plugin.ServeConfig{
			HandshakeConfig: pkgplugin.Handshake,
			Plugins: map[string]plugin.Plugin{
				"ds-plugin": &pkgplugin.DSPlugin{Impl: porterPlugin},
			},
			GRPCServer: plugin.DefaultGRPCServer,
		})
		return
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	// Check for special flags
	if os.Args[1] == "--version" {
		fmt.Printf("porter version %s\n", version)
		fmt.Printf("  commit: %s\n", commit)
		fmt.Printf("  built:  %s\n", date)
		return
	}

	if os.Args[1] == "--manifest" {
		printManifest()
		return
	}

	if os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "help" {
		printUsage()
		return
	}

	if os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "help" {
		printUsage()
		return
	}

	// Parse DS plugin config from environment
	config, err := porter.LoadConfigFromEnv()
	if err != nil {
		logger.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	client, err := porter.NewClient(config, logger)
	if err != nil {
		logger.Error("Failed to create porter client", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	operation := os.Args[1]
	args := os.Args[2:]

	if err := executeOperation(client, operation, args, logger, os.Stdout); err != nil {
		logger.Error("Operation failed", "operation", operation, "error", err)
		os.Exit(1)
	}
}

func executeOperation(client *porter.Client, operation string, args []string, logger hclog.Logger, stdout io.Writer) error {
	switch operation {
	case "pull":
		return handlePull(client, args, logger, stdout)
	case "push":
		return handlePush(client, args, logger, stdout)
	case "list":
		return handleList(client, args, logger, stdout)
	case "execute-plugin":
		return handleExecutePlugin(client, args, logger, stdout)
	default:
		return fmt.Errorf("unknown operation: %s", operation)
	}
}

func handlePull(client *porter.Client, args []string, logger hclog.Logger, stdout io.Writer) error {
	for _, arg := range args {
		if isHelpFlag(arg) {
			printPullUsage(stdout)
			return nil
		}
	}

	if len(args) < 1 {
		printPullUsage(stdout)
		return fmt.Errorf("artifact reference required")
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
			return nil
		} else if arg == "--insecure" {
			insecure = true
		} else if arg == "-o" || arg == "--output" {
			if i+1 < len(args) {
				output = args[i+1]
				i++ // skip next arg
			} else {
				return fmt.Errorf("output path required for %s", arg)
			}
		} else if arg == "--all-arch" {
			allPlatforms = true
		} else if arg == "--platform" {
			if i+1 >= len(args) {
				return fmt.Errorf("platform value required for %s", arg)
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
		return fmt.Errorf("artifact reference required")
	}

	if allPlatforms && len(platformSelections) > 0 {
		return fmt.Errorf("--all-arch cannot be combined with --platform")
	}

	result, err := client.PullArtifact(ref, insecure)
	if err != nil {
		return err
	}

	// If output is specified, export the artifact
	if output != "" {
		exportOpts, err := buildExportOptions(allPlatforms, platformSelections)
		if err != nil {
			return err
		}

		exportedPaths, err := client.ExportArtifact(result, output, exportOpts)
		if err != nil {
			return fmt.Errorf("failed to export artifact: %w", err)
		}
		result.ExportedFiles = exportedPaths
		for _, p := range exportedPaths {
			logger.Info("Artifact exported", "path", p)
		}
	}

	// Output result as JSON for DS to parse
	jsonOutput, _ := json.Marshal(result)
	fmt.Fprintln(stdout, string(jsonOutput))
	return nil
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
	fmt.Fprintln(w, "Usage: ds porter pull [flags] <artifact-ref>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --output, -o <path>   Export artifact to a file or directory")
	fmt.Fprintln(w, "                         Directories receive ds-porter by default; files write the binary directly")
	fmt.Fprintln(w, "  --platform <os/arch>  Fetch a specific platform (repeatable; e.g. linux/arm64)")
	fmt.Fprintln(w, "  --all-arch            Fetch every platform in the index (requires directory output)")
	fmt.Fprintln(w, "  --insecure            Allow plain HTTP connections to registries")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Behaviour:")
	fmt.Fprintln(w, "  • Without --platform/--all-arch, the current runtime platform is exported")
	fmt.Fprintln(w, "  • When multiple platforms are requested, artifacts are written to <dir>/<os>/<arch>/")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  ds porter pull ghcr.io/delivery-station/porter:0.2.0 -o ./porter-bin")
	fmt.Fprintln(w, "  ds porter pull localhost/delivery-station/porter:0.2.0 --platform linux/arm64 -o ./out")
	fmt.Fprintln(w, "  ds porter pull ghcr.io/...:0.2.0 --all-arch -o ./artifacts")
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
	for _, arg := range args {
		if len(arg) > 11 && arg[:11] == "--manifest=" {
			manifestPath = arg[11:]
		} else if arg == "--insecure" {
			insecure = true
		} else {
			positionalArgs = append(positionalArgs, arg)
		}
	}

	// Multi-arch push via manifest
	if manifestPath != "" {
		if len(positionalArgs) < 1 {
			return fmt.Errorf("registry reference required")
		}
		ref = positionalArgs[0]
		return handleMultiArchPush(ref, manifestPath, logger, stdout, insecure)
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

	output, _ := json.Marshal(result)
	fmt.Fprintln(stdout, string(output))
	return nil
}

func handleMultiArchPush(ref, manifestPath string, logger hclog.Logger, stdout io.Writer, insecure bool) error {
	// Parse registry and repository from ref
	// ref format: registry/repo[:tag]
	// We need to split this for ReleaseConfig
	// Actually, let's just pass the full ref and let the pusher handle it

	// Get credentials from env
	username := os.Getenv("REGISTRY_USERNAME")
	password := os.Getenv("REGISTRY_PASSWORD")
	if password == "" {
		password = os.Getenv("GITHUB_TOKEN")
	}

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

	output, _ := json.Marshal(artifacts)
	fmt.Fprintln(stdout, string(output))
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

/*
func handleRelease(args []string, logger hclog.Logger) error {
	if len(args) < 2 {
		return fmt.Errorf("version and registry required\nUsage: release <version> <registry> [--username=<user>] [--password=<pass>]")
	}

	version := args[0]
	registry := args[1]

	// Parse optional flags
	var username, password, manifestPath string
	for _, arg := range args[2:] {
		if len(arg) > 11 && arg[:11] == "--username=" {
			username = arg[11:]
		} else if len(arg) > 11 && arg[:11] == "--password=" {
			password = arg[11:]
		} else if len(arg) > 11 && arg[:11] == "--manifest=" {
			manifestPath = arg[11:]
		}
	}

	// If no password provided, try environment
	if password == "" {
		password = os.Getenv("GITHUB_TOKEN")
		if password == "" {
			password = os.Getenv("REGISTRY_PASSWORD")
		}
	}

	// Get git commit
	commit := "unknown"
	if cmd := exec.Command("git", "rev-parse", "--short", "HEAD"); cmd.Run() == nil {
		if output, err := cmd.Output(); err == nil {
			commit = string(output)
		}
	}

	// Build config
	buildConfig := release.BuildConfig{
		Version:    version,
		BinaryName: "porter-ds",
		SourceDir:  "./cmd/porter",
		OutputDir:  "bin/release",
		LDFlags:    fmt.Sprintf("-s -w -X main.version=%s -X main.commit=%s", version, commit),
		Commit:     commit,
	}

	// Release config
	releaseConfig := release.ReleaseConfig{
		Registry:     registry,
		Repository:   "delivery-station/porter",
		Version:      version,
		Username:     username,
		Password:     password,
		TagLatest:    true,
		ManifestPath: manifestPath,
	}

	// Create release orchestrator
	rel, err := release.NewRelease(buildConfig, releaseConfig)
	if err != nil {
		return fmt.Errorf("failed to create release: %w", err)
	}

	// Execute release
	ctx := context.Background()
	if err := rel.Execute(ctx, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("release failed: %w", err)
	}

	return nil
}
*/

func printUsage() {
	fmt.Println("Porter - OCI Artifact Management Plugin for DS")
	fmt.Println()
	fmt.Println("Usage: ds-porter <operation> [args]")
	fmt.Println()
	fmt.Println("Operations:")
	fmt.Println("  pull <ref>              Pull artifact from OCI registry")
	fmt.Println("  push <path> <ref>       Push artifact to OCI registry")
	fmt.Println("  list                    List cached artifacts")
	fmt.Println("  execute-plugin <id> <plugin> [args...]")
	fmt.Println("                         Execute plugin on artifact")
	fmt.Println()
	fmt.Println("Pull flags:")
	fmt.Println("  --output, -o <path>     Export artifact to file or directory")
	fmt.Println("  --platform <os/arch>    Fetch specific platform (repeatable)")
	fmt.Println("  --all-arch              Fetch every platform in the index")
	fmt.Println("  --insecure              Allow plain HTTP registry access")
	fmt.Println()
	fmt.Println("Multi-arch Push:")
	fmt.Println("  ds-porter push <registry-ref> --manifest=<path>")
	fmt.Println()
	fmt.Println("Special flags:")
	fmt.Println("  --version               Print version")
	fmt.Println("  --manifest              Print plugin manifest")
}

func printManifest() {
	manifest := map[string]interface{}{
		"name":        "porter",
		"version":     "0.1.0",
		"description": "OCI artifact management plugin for Delivery Station",
		"platform": map[string][]string{
			"os":   {"linux", "darwin", "windows"},
			"arch": {"amd64", "arm64"},
		},
		"operations": []string{"pull", "push", "list", "execute-plugin"},
		"config": map[string]interface{}{
			"registries": "List of OCI registry configurations",
			"cache_dir":  "Local cache directory for artifacts",
		},
	}

	output, _ := json.MarshalIndent(manifest, "", "  ")
	fmt.Println(string(output))
}
