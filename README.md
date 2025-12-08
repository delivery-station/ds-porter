# Delivery Station Porter Plugin

`ds-porter` is the Delivery Station (DS) plugin that knows how to discover, download, cache, and expose Porter artifacts from OCI registries. It can be invoked directly as a standalone CLI or loaded by the DS host process via the HashiCorp go-plugin protocol.

## Key Capabilities

- **OCI artifact pull** – Download Porter bundles and metadata using ORAS with optional insecure transport for local registries.
- **Platform-aware exports** – Materialize binaries for the current platform by default, or opt into specific architectures with `--platform` or `--all-arch`.
- **Content caching** – Store pulled artifacts under the DS cache directory and rehydrate metadata for subsequent commands.
- **Plugin delegation** – Surface artifact information back to DS so other plugins can be chained.

## Prerequisites

- Go 1.21+
- (optional) Docker/`oras` if you plan to interact with local registries defined in `docker-compose.yaml`

## Building & Running

```bash
# Fetch dependencies and build
make build

# Run the CLI directly
./bin/ds-porter pull ghcr.io/delivery-station/porter:0.2.0 -o ./out

# Execute tests
make test
```

When DS launches the plugin it injects configuration through the `DS_PORTER_CONFIG` environment variable. Running the binary manually falls back to `~/.ds/porter-cache`.

## CLI Reference

| Command | Description |
| ------- | ----------- |
| `pull <ref>` | Pull an artifact from an OCI registry and optionally export it locally. |
| `list` | Print cached artifact metadata stored under the Porter cache. |
| `execute-plugin <artifact-id> <plugin> [args...]` | Request DS to execute another plugin against a cached artifact. |
| `push` | Reserved; push support via ORAS is planned but not implemented today. |

### `pull` command

```
Usage: ds porter pull [flags] <artifact-ref>

Flags:
  --output, -o <path>    Export artifact to a file or directory
                         • default behaviour writes the current platform binary as <path>/ds-porter
                         • if <path> points to a file (e.g. ./porter.exe) the binary is written directly
  --platform <os/arch>   Fetch a specific platform (repeatable, e.g. --platform linux/arm64)
  --all-arch             Fetch every platform from the OCI index (requires directory output)
  --insecure             Allow plain HTTP access to registries (useful for localhost testing)
  -h, --help             Show pull-specific usage

Behaviour:
- No platform flags → only the current runtime platform is exported.
- Multiple platforms → exported under `<output>/<os>/<arch>/ds-porter` (variant adds another directory).
- Registry credentials and mirror configuration are provided by DS via `DS_PORTER_CONFIG`.

Examples
---------
```
# Fetch the current platform into ./dist/ds-porter
ds porter pull ghcr.io/delivery-station/porter:0.2.0 -o ./dist

# Export a Windows build into a specific file
ds porter pull localhost/delivery-station/porter:0.2.0 --insecure --platform windows/amd64 -o ./porter.exe

# Download every architecture into sub-folders
ds porter pull ghcr.io/delivery-station/porter:0.2.0 --all-arch -o ./artifacts
```

### `list`

```
ds porter list | jq
```

Returns an array of cached artifact descriptors (`id`, `reference`, `digest`, timestamps) sourced from the local cache directory.

### `execute-plugin`

```
ds porter execute-plugin <artifact-id> <plugin> [args...]
```

This call is primarily used by Delivery Station itself. It records the intent to execute another plugin against the cached artifact; DS performs the actual invocation.

### `push`

```
ds porter push path/to/porter.manifest.yaml ghcr.io/delivery-station/porter:0.2.0
```

Pushes a plugin artifact to the target registry. Provide a manifest (see `examples/`) describing the
binary, including optional `platform` metadata and annotations. Relative paths are resolved from the
manifest directory. If a manifest is not provided, the command treats the first argument as a direct
binary path for the current platform.

## Release Builds

Use `make release-build-all` to produce multi-platform binaries that match `ds.manifest.yaml`.

```
make release-build-all VERSION=v0.2.0

bin/
├── darwin/
│   ├── amd64/ds-porter
│   └── arm64/ds-porter
├── linux/
│   ├── amd64/ds-porter
│   └── arm64/ds-porter
└── windows/
    ├── amd64/ds-porter.exe
    └── arm64/ds-porter.exe
```

These paths are referenced directly in `ds.manifest.yaml`, so do not change the layout without updating the manifest.

## Project Layout

```
porter/
├── cmd/porter/            # go-plugin entry point and CLI wiring
├── pkg/porter/            # Artifact pull/cache logic (ORAS based)
├── internal/adapter/      # Optional DS client adapter for Porter-specific flows
├── internal/storage/      # Lightweight JSON installation store used by adapter
├── bin/                   # Build output (created by make)
└── registry/              # Test OCI registry content for local development
```

The adapter and storage packages remain for higher-level Porter workflows and will evolve as the DS integration expands.

## Configuration Reference

Delivery Station serialises configuration into `DS_PORTER_CONFIG` before launching the plugin. The structure mirrors the DS core config:

```json
{
  "cache_dir": "/Users/alex/.cache/ds/porter",
  "registries": [
    {
      "name": "default",
      "url": "ghcr.io",
      "username": "${GITHUB_USER}",
      "token": "${GITHUB_TOKEN}"
    }
  ]
}
```

When developing locally you can set the environment variable manually or rely on the default cache location.

## Development Workflow

- Format code with `gofmt` or `make lint`.
- Run the full test suite with `go test ./...`.
- Use `docker-compose up` to start the local registry defined in `docker-compose.yaml` for end-to-end flows.

## License

MIT
