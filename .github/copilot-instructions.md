# GitHub Copilot Instructions for Porter Plugin

## Project Overview

**Porter** is a reference plugin for the DS (Delivery Station) CLI framework. Porter's purpose is to fetch OCI artifacts from registries, download them locally to the DS cache, and then execute appropriate delivery plugins based on the artifact's manifest metadata.

## Porter's Role in DS Ecosystem

Porter is the **primary artifact handler** in the DS plugin ecosystem:
1. **Fetches** OCI artifacts from container registries (GitHub, Docker Hub, etc.)
2. **Caches** artifacts locally using DS shared cache
3. **Analyzes** artifact manifests to determine delivery requirements
4. **Orchestrates** delivery by calling appropriate DS plugins via gRPC
5. **Chains** multiple delivery plugins when complex workflows are needed

### Porter as a DS gRPC Plugin

Porter is a **standalone Go binary** that implements the DS gRPC plugin interface:
- Is named `ds-porter` (follows DS plugin naming convention)
- Communicates with DS via **HashiCorp go-plugin** gRPC protocol
- Implements the DS Plugin gRPC service interface
- Receives DS configuration via gRPC ExecuteRequest messages
- Uses gRPC broker to invoke other DS plugins for delivery operations
- Automatic health checks and lifecycle management by DS host

### Workflow

```
User Command: ds porter fetch ghcr.io/org/app:v1.0.0
     ↓
DS Host loads porter plugin via gRPC
     ↓
DS calls Porter.Execute(command="fetch", args=[...])
     ↓
Porter receives configuration via gRPC ExecuteRequest
     ↓
1. Read DS configuration from request config map
     ↓
2. Authenticate to OCI registry
     ↓
3. Download artifact (layers, manifest, config)
     ↓
4. Store in DS cache (~/.cache/ds/)
     ↓
5. Parse manifest for delivery instructions
     ↓
6. Request other plugins via gRPC broker if needed
     ↓
7. Return ExecuteResponse with results
```

## Commands

### porter fetch
Downloads an OCI artifact from registry to local cache.

```bash
ds porter fetch <artifact-reference> [flags]

# Examples:
ds porter fetch ghcr.io/org/app:v1.0.0
ds porter fetch ghcr.io/org/app@sha256:abc123...
ds porter fetch ghcr.io/org/app:latest --force  # Force re-download
```

**Behavior**:
1. Parse artifact reference (registry/repo:tag or @digest)
2. Check DS cache for existing artifact
3. If not cached or --force flag, download from registry
4. Validate artifact integrity (checksums)
5. Store in cache with metadata
6. Print artifact info (size, layers, manifest)

### porter run
Executes a local artifact (for artifacts containing executables).

```bash
ds porter run <artifact-reference> [-- args]

# Examples:
ds porter run ghcr.io/org/tool:v1.0.0
ds porter run ghcr.io/org/tool:v1.0.0 -- --help
ds porter run local-path/artifact.tar
```

**Behavior**:
1. Fetch artifact if not cached
2. Extract artifact contents
3. Determine entrypoint (from manifest or convention)
4. Execute with provided arguments
5. Stream output to stdout/stderr
6. Return exit code

### porter deliver
Delivers artifact to destination based on manifest annotations.

```bash
ds porter deliver <artifact-reference> [flags]

# Examples:
ds porter deliver ghcr.io/org/app:v1.0.0
ds porter deliver ghcr.io/org/app:v1.0.0 --dry-run
```

**Behavior**:
1. Fetch artifact if not cached
2. Read artifact manifest annotations
3. Determine delivery method(s) from annotations:
   - `delivery.ds/s3`: Call `ds s3-uploader`
   - `delivery.ds/http`: Call `ds http-publisher`
   - `delivery.ds/pipeline`: Chain multiple plugins
4. Invoke delivery plugin(s) with artifact path
5. Aggregate results from delivery plugins
6. Report final status

### porter apply
Combined operation: fetch + deliver in one command.

```bash
ds porter apply <artifact-reference> [flags]

# Examples:
ds porter apply ghcr.io/org/app:v1.0.0
ds porter apply ghcr.io/org/app:v1.0.0 --wait
```

**Behavior**:
1. Execute `fetch` operation
2. Execute `deliver` operation
3. Report combined status

## Artifact Manifest Annotations

Porter reads standard OCI manifest annotations to determine delivery:

```json
{
  "annotations": {
    "delivery.ds/method": "s3",
    "delivery.ds/s3.bucket": "my-artifacts",
    "delivery.ds/s3.prefix": "releases/",
    "delivery.ds/s3.region": "us-east-1",
    
    "delivery.ds/pipeline": "s3,http",
    "delivery.ds/http.endpoint": "https://api.example.com/artifacts",
    "delivery.ds/http.method": "POST"
  }
}
```

### Supported Delivery Methods
- `s3`: Amazon S3 upload
- `http`: HTTP/REST API push
- `file`: Local filesystem copy
- `pipeline`: Chain multiple methods

## Technical Stack

### Core Dependencies
```go
import (
    // DS plugin framework
    "github.com/hashicorp/go-plugin"
    "github.com/hashicorp/go-hclog"
    "google.golang.org/grpc"
    
    // DS client library and types
    "github.com/delivery-station/ds/pkg/client"
    "github.com/delivery-station/ds/pkg/types"
    
    // OCI client
    "oras.land/oras-go/v2"
    "oras.land/oras-go/v2/content/oci"
    
    // CLI framework (for local testing)
    "github.com/spf13/cobra"
    
    // OCI spec
    "github.com/opencontainers/image-spec/specs-go/v1"
)
```

## Code Organization

```
porter/
├── .github/
│   ├── copilot-instructions.md  (this file)
│   └── workflows/
│       ├── ci.yaml
│       └── release.yaml
├── cmd/
│   └── porter/
│       └── main.go              # Entry point
├── internal/
│   ├── fetch/                   # Fetch command logic
│   │   ├── fetcher.go
│   │   └── cache.go
│   ├── run/                     # Run command logic
│   │   ├── executor.go
│   │   └── extract.go
│   ├── deliver/                 # Deliver command logic
│   │   ├── deliverer.go
│   │   ├── parser.go           # Parse manifest annotations
│   │   └── dispatcher.go       # Dispatch to delivery plugins
│   └── common/                  # Shared utilities
│       ├── artifact.go          # Artifact handling
│       └── manifest.go          # Manifest parsing
├── pkg/
│   └── porter/                  # Public API (if needed)
├── plugin.yaml                  # Porter plugin manifest
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

## Development Guidelines

### Implementing the gRPC Plugin Interface

Porter must implement the DS Plugin gRPC service:

```go
package main

import (
    "context"
    "github.com/hashicorp/go-plugin"
    "github.com/delivery-station/ds/pkg/types"
    "google.golang.org/grpc"
)

// PorterPlugin is the plugin implementation
type PorterPlugin struct {
    plugin.Plugin
    impl *PorterPluginServer
}

func (p *PorterPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
    types.RegisterPluginServer(s, p.impl)
    return nil
}

func (p *PorterPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
    return types.NewPluginClient(c), nil
}

// PorterPluginServer implements the gRPC service interface
type PorterPluginServer struct {
    types.UnimplementedPluginServer
    broker *plugin.GRPCBroker
}

func (s *PorterPluginServer) Execute(ctx context.Context, req *types.ExecuteRequest) (*types.ExecuteResponse, error) {
    // Route to appropriate command handler
    switch req.Command {
    case "fetch":
        return s.handleFetch(ctx, req)
    case "run":
        return s.handleRun(ctx, req)
    case "deliver":
        return s.handleDeliver(ctx, req)
    case "apply":
        return s.handleApply(ctx, req)
    default:
        return &types.ExecuteResponse{
            ExitCode: 1,
            Error: "unknown command: " + req.Command,
        }, nil
    }
}

func (s *PorterPluginServer) GetMetadata(ctx context.Context, req *types.Empty) (*types.PluginMetadata, error) {
    return &types.PluginMetadata{
        Name: "porter",
        Version: "1.0.0",
        ProtocolVersion: 1,
        Commands: []*types.CommandInfo{
            {Name: "fetch", Description: "Download artifact from OCI registry"},
            {Name: "run", Description: "Execute artifact locally"},
            {Name: "deliver", Description: "Deliver artifact to destination"},
            {Name: "apply", Description: "Fetch and deliver in one operation"},
        },
    }, nil
}

func (s *PorterPluginServer) Health(ctx context.Context, req *types.Empty) (*types.HealthResponse, error) {
    return &types.HealthResponse{
        Healthy: true,
        Message: "porter plugin is healthy",
    }, nil
}

func main() {
    plugin.Serve(&plugin.ServeConfig{
        HandshakeConfig: types.HandshakeConfig,
        Plugins: map[string]plugin.Plugin{
            "porter": &PorterPlugin{
                impl: &PorterPluginServer{},
            },
        },
        GRPCServer: plugin.DefaultGRPCServer,
    })
}
```

### Configuration Access

Porter receives configuration via gRPC ExecuteRequest:

```go
func (s *PorterPluginServer) handleFetch(ctx context.Context, req *types.ExecuteRequest) (*types.ExecuteResponse, error) {
    // Extract configuration from request
    registry := req.Config["registry.default"]
    cacheDir := req.Config["cache.dir"]
    dockerConfig := req.Config["auth.docker_config"]
    logLevel := req.Config["logging.level"]
    
    // Setup logger
    logger := hclog.New(&hclog.LoggerOptions{
        Level: hclog.LevelFromString(logLevel),
        Output: os.Stderr,
    })
    
    // Parse arguments
    if len(req.Args) == 0 {
        return &types.ExecuteResponse{
            ExitCode: 1,
            Error: "missing artifact reference",
        }, nil
    }
    
    artifactRef := req.Args[0]
    
    // Fetch logic...
    logger.Info("fetching artifact", "ref", artifactRef, "registry", registry)
    
    // Return response
    return &types.ExecuteResponse{
        ExitCode: 0,
        Stdout: []byte("Successfully fetched " + artifactRef),
    }, nil
}
```

### Calling Other Plugins via gRPC Broker

Porter invokes delivery plugins through the gRPC broker:

```go
func (s *PorterPluginServer) deliverToS3(ctx context.Context, artifactPath string, config map[string]string) error {
    // Get the broker connection ID for s3-uploader plugin
    conn, err := s.broker.Dial(s.s3PluginID)
    if err != nil {
        return fmt.Errorf("failed to connect to s3-uploader: %w", err)
    }
    defer conn.Close()
    
    // Create gRPC client for the plugin
    client := types.NewPluginClient(conn)
    
    // Call the s3-uploader plugin
    resp, err := client.Execute(ctx, &types.ExecuteRequest{
        Command: "upload",
        Args: []string{
            "--bucket=" + config["delivery.ds/s3.bucket"],
            "--prefix=" + config["delivery.ds/s3.prefix"],
            "--file=" + artifactPath,
        },
        Config: config, // Pass through DS configuration
    })
    
    if err != nil {
        return fmt.Errorf("s3 upload failed: %w", err)
    }
    
    if resp.ExitCode != 0 {
        return fmt.Errorf("s3 upload failed with exit code %d: %s", resp.ExitCode, resp.Error)
    }
    
    return nil
}

func (s *PorterPluginServer) handleDeliver(ctx context.Context, req *types.ExecuteRequest) (*types.ExecuteResponse, error) {
    // Fetch artifact first
    artifactPath, err := s.fetchArtifact(ctx, req.Args[0], req.Config)
    if err != nil {
        return &types.ExecuteResponse{
            ExitCode: 1,
            Error: err.Error(),
        }, nil
    }
    
    // Parse manifest annotations
    deliveryMethod := req.Config["delivery.ds/method"]
    
    // Dispatch to appropriate delivery plugin
    switch deliveryMethod {
    case "s3":
        err = s.deliverToS3(ctx, artifactPath, req.Config)
    case "http":
        err = s.deliverToHTTP(ctx, artifactPath, req.Config)
    default:
        return &types.ExecuteResponse{
            ExitCode: 1,
            Error: "unsupported delivery method: " + deliveryMethod,
        }, nil
    }
    
    if err != nil {
        return &types.ExecuteResponse{
            ExitCode: 1,
            Error: err.Error(),
        }, nil
    }
    
    return &types.ExecuteResponse{
        ExitCode: 0,
        Stdout: []byte("Successfully delivered artifact"),
    }, nil
}
```

### Error Handling

Always provide context in errors:

```go
// Good
if err != nil {
    return fmt.Errorf("failed to fetch artifact %s: %w", ref, err)
}

// Bad
if err != nil {
    return err
}
```

### Logging

Use structured logging:

```go
logger.WithFields(log.Fields{
    "artifact": ref,
    "registry": registry,
    "cached": isCached,
}).Info("fetching artifact")
```

### Cache Operations

Use DS cache configuration from gRPC request:

```go
func (s *PorterPluginServer) fetchArtifact(ctx context.Context, ref string, config map[string]string) (string, error) {
    cacheDir := config["cache.dir"]
    
    // Check cache
    cachePath := filepath.Join(cacheDir, "artifacts", hashRef(ref))
    if _, err := os.Stat(cachePath); err == nil {
        s.logger.Info("artifact found in cache", "ref", ref)
        return cachePath, nil
    }
    
    // Download artifact
    artifact, err := s.downloadArtifact(ctx, ref, config)
    if err != nil {
        return "", fmt.Errorf("failed to download: %w", err)
    }
    
    // Store in cache
    if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
        s.logger.Warn("failed to create cache directory", "error", err)
        // Don't fail, caching is best-effort
    } else if err := os.WriteFile(cachePath, artifact, 0644); err != nil {
        s.logger.Warn("failed to cache artifact", "error", err)
        // Don't fail, caching is best-effort
    }
    
    return cachePath, nil
}
```

## Plugin Manifest (plugin.yaml)

```yaml
name: porter
version: 1.0.0
protocol_version: 1
description: Fetch and deliver OCI artifacts from registries
author: Delivery Station Team
homepage: https://github.com/delivery-station/porter

commands:
  - name: fetch
    description: Download artifact from OCI registry to local cache
    usage: "ds porter fetch <artifact-reference> [flags]"
    
  - name: run
    description: Execute artifact locally
    usage: "ds porter run <artifact-reference> [-- args]"
    
  - name: deliver
    description: Deliver artifact to destination based on manifest
    usage: "ds porter deliver <artifact-reference> [flags]"
    
  - name: apply
    description: Fetch and deliver in one operation
    usage: "ds porter apply <artifact-reference> [flags]"

platform:
  os: [linux, darwin, windows]
  arch: [amd64, arm64]

requirements:
  ds_version: ">=1.0.0"
  protocol_version: 1

dependencies:
  - plugin: s3-uploader
    optional: true
    description: "For S3 delivery"
  - plugin: http-publisher
    optional: true
    description: "For HTTP delivery"
```

## Implementation Tasks

### Phase 1: Project Setup
1. Initialize Go module: `go mod init github.com/delivery-station/porter`
2. Add DS client library as dependency
3. Add HashiCorp go-plugin dependency
4. Setup gRPC plugin infrastructure
5. Create plugin.yaml manifest with protocol_version
6. Setup basic logging with hclog
7. Create README.md

### Phase 2: gRPC Plugin Implementation
1. Implement PorterPluginServer with gRPC service methods
2. Implement Execute() method with command routing
3. Implement GetMetadata() for plugin information
4. Implement Health() for health checks
5. Setup plugin.Serve() in main()
6. Handle configuration from ExecuteRequest.Config
7. Unit tests for gRPC handlers

### Phase 3: Fetch Command Implementation
1. Parse artifact reference from ExecuteRequest.Args
2. Extract cache config from ExecuteRequest.Config
3. Setup ORAS client with authentication from config
4. Implement artifact download logic
5. Store artifact in cache
6. Return artifact info in ExecuteResponse.Stdout
7. Handle --force flag via args parsing
8. Unit tests for fetch logic

### Phase 4: Run Command Implementation
1. Reuse fetch logic to get artifact
2. Extract artifact contents to temp directory
3. Parse manifest for entrypoint
4. Execute binary/script
5. Capture output in ExecuteResponse
6. Cleanup temp files
7. Return proper exit codes
8. Unit tests for run logic

### Phase 5: Deliver Command with gRPC Broker
1. Reuse fetch logic to get artifact
2. Parse manifest annotations from config
3. Setup gRPC broker for plugin-to-plugin communication
4. Implement delivery dispatcher using broker:
   - Map annotation to plugin name
   - Dial plugin via broker
   - Create gRPC client
   - Execute delivery via gRPC call
5. Support multiple delivery methods (pipeline)
6. Aggregate delivery results in ExecuteResponse
7. Add `--dry-run` flag via args
8. Integration tests with mock plugins

### Phase 5: Apply Command
1. Combine fetch + deliver logic in single Execute handler
2. Add `--wait` flag for async operations
3. Report combined status
4. Handle partial failures
5. Unit tests

### Phase 6: Advanced Features
1. Progress indicators for large downloads
2. Resume interrupted downloads
3. Parallel layer downloads
4. Signature verification (future)
5. Delivery retry logic
6. Plugin health checks before dispatch

## Testing Strategy

### Unit Tests
- Artifact reference parsing
- Manifest annotation parsing
- Cache key generation
- Delivery method mapping
- Error handling

### Integration Tests
- Fetch from real OCI registry (GitHub)
- Cache storage and retrieval
- Plugin invocation (with mock plugins)
- End-to-end workflows

### Test Fixtures
```
testdata/
├── manifests/           # Sample OCI manifests
├── artifacts/           # Sample artifacts
├── plugins/            # Mock delivery plugins
└── configs/            # Test configurations
```

### Mock Delivery Plugin
Create a simple mock plugin for testing:

```bash
#!/bin/bash
# testdata/plugins/ds-test-uploader
echo "Mock delivery: $@"
exit 0
```

## Configuration Examples

Porter uses DS configuration automatically but can have plugin-specific settings:

```yaml
# In DS config (~/.config/ds/config.yaml)
plugins:
  porter:
    retry_attempts: 3
    timeout: 300
    parallel_downloads: true
    max_connections: 5
```

## Command Examples

### Fetch Artifact
```bash
# Fetch latest version
ds porter fetch ghcr.io/org/myapp:latest

# Fetch specific version
ds porter fetch ghcr.io/org/myapp:v1.2.3

# Fetch by digest
ds porter fetch ghcr.io/org/myapp@sha256:abc123...

# Force re-download
ds porter fetch ghcr.io/org/myapp:latest --force

# With debug logging
ds --log-level=debug porter fetch ghcr.io/org/myapp:latest
```

### Run Artifact
```bash
# Run tool from artifact
ds porter run ghcr.io/org/tool:v1.0.0

# Pass arguments to tool
ds porter run ghcr.io/org/tool:v1.0.0 -- --help

# Run with specific entrypoint
ds porter run ghcr.io/org/tool:v1.0.0 --entrypoint=/bin/custom-script
```

### Deliver Artifact
```bash
# Deliver based on manifest
ds porter deliver ghcr.io/org/app:v1.0.0

# Dry run (show what would happen)
ds porter deliver ghcr.io/org/app:v1.0.0 --dry-run

# Override delivery method
ds porter deliver ghcr.io/org/app:v1.0.0 --method=s3 --bucket=my-bucket
```

### Apply (Fetch + Deliver)
```bash
# One-step deployment
ds porter apply ghcr.io/org/app:v1.0.0

# Wait for completion
ds porter apply ghcr.io/org/app:v1.0.0 --wait
```

## Output Format

Porter should provide clear, structured output:

```
$ ds porter fetch ghcr.io/org/app:v1.0.0

Fetching artifact: ghcr.io/org/app:v1.0.0
├─ Checking cache... not found
├─ Authenticating to ghcr.io... ✓
├─ Downloading manifest... ✓
├─ Downloading layers (3 layers, 45.2 MB)...
│  ├─ Layer 1/3: 15.1 MB [████████████████] 100%
│  ├─ Layer 2/3: 20.3 MB [████████████████] 100%
│  └─ Layer 3/3: 9.8 MB  [████████████████] 100%
├─ Verifying checksums... ✓
└─ Storing in cache... ✓

Artifact Details:
  Reference: ghcr.io/org/app:v1.0.0
  Digest: sha256:abc123...
  Size: 45.2 MB
  Cached at: ~/.cache/ds/artifacts/abc123

✓ Successfully fetched artifact
```

## Release & Distribution

### Building
```bash
# Build for current platform
make build

# Build for all platforms
make build-all

# Output: dist/ds-porter-<os>-<arch>
```

### Publishing to OCI Registry
```bash
# Login to GitHub Container Registry
echo $GITHUB_TOKEN | oras login ghcr.io -u $GITHUB_USER --password-stdin

# Push plugin artifact
oras push ghcr.io/delivery-station/plugins/porter:1.0.0 \
  ./dist/ds-porter-linux-amd64:application/vnd.ds.plugin.binary.linux.amd64 \
  ./dist/ds-porter-darwin-amd64:application/vnd.ds.plugin.binary.darwin.amd64 \
  ./dist/ds-porter-windows-amd64.exe:application/vnd.ds.plugin.binary.windows.amd64 \
  ./plugin.yaml:application/vnd.ds.plugin.manifest
```

### Installation by Users
```bash
# DS will install porter automatically or manually:
ds plugin install porter
ds plugin install porter@1.0.0
```

## Security Considerations

1. **Artifact Verification**: Validate checksums of downloaded artifacts
2. **Authentication**: Use DS auth configuration, never hardcode credentials
3. **Execution Safety**: Validate artifacts before execution
4. **Plugin Trust**: Only invoke known delivery plugins
5. **Path Traversal**: Sanitize artifact paths during extraction
6. **Resource Limits**: Set timeouts and size limits

## Success Criteria

Porter is complete when:
1. ✓ Implements DS gRPC Plugin service interface
2. ✓ Can fetch artifacts from GitHub Container Registry
3. ✓ Uses DS cache from configuration
4. ✓ Reads DS configuration from ExecuteRequest
5. ✓ Can execute local artifacts
6. ✓ Parses manifest annotations correctly
7. ✓ Invokes delivery plugins via gRPC broker
8. ✓ Supports plugin chaining for complex delivery
9. ✓ All commands return proper ExecuteResponse
10. ✓ Unit and integration tests pass
11. ✓ Documentation is complete
12. ✓ Published to OCI registry
13. ✓ Can be loaded via DS plugin system

## Future Enhancements

- Signature verification (cosign integration)
- Artifact scanning (vulnerability checks)
- Parallel artifact processing
- Webhook notifications on delivery
- Delivery rollback support
- Custom delivery plugin development SDK

---

**Remember**: Porter is the reference plugin and must properly implement the HashiCorp go-plugin gRPC interface. Keep implementation clean, well-documented, and exemplary for future plugin developers. Use gRPC for all DS communication.
