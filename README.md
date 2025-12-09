## Delivery Station Porter Plugin

`ds-porter` is the Delivery Station (DS) plugin responsible for discovering, downloading, caching, and exporting Porter artifacts from OCI registries. The binary is designed to be launched exclusively by DS via the HashiCorp go-plugin protocol.

## Highlights
- **OCI fetch & export** – Pull artifacts with ORAS, export the current platform by default, or select specific architectures with `--platform` or `--all-arch`.
- **Shared caching** – Populate and reuse the DS cache so subsequent plugin invocations can operate offline.
- **Registry-aware** – Respect registry mirrors, authentication, and insecure endpoints supplied by DS configuration.
- **Plugin chaining** – Relay artifact data back to DS so other plugins can iterate on the same payload.
- **Artifact publishing** – Push single-platform binaries or multi-arch bundles described by `ds.manifest.yaml`.

## Use With Delivery Station

1. Install the plugin through DS if it is not already available:
   ```bash
   ds plugin install porter
   ```
2. Invoke Porter commands through DS:
   ```bash
   ds porter pull ghcr.io/delivery-station/porter:0.2.0 -o ./dist
   ds porter list
   ds porter push --manifest=ds.manifest.yaml ghcr.io/delivery-station/porter:0.2.0
   ```

DS injects configuration via the shared `types.Config` contract (surfaced to Porter as `DS_*` environment variables). When the plugin runs under DS, it automatically picks up cache directories, registry credentials, logging level, and proxy settings from the host.

## Command Reference

| Command | Summary |
| --- | --- |
| `pull <ref> [flags]` | Fetch an artifact from an OCI registry and optionally export binaries. |
| `push [--manifest=<path>] <src> <ref>` | Push a binary or manifest-defined bundle to a registry. |
| `list` | Return cached artifact descriptors as JSON. |
| `execute-plugin <artifact-id> <plugin> [args…]` | Request DS to hand a cached artifact to another plugin. |

### Pull
```
ds porter pull [--output|-o <path>] [--platform <os/arch>] [--all-arch] [--insecure] <ref>
```
- No flags exports the current platform.
- Repeating `--platform` writes binaries under `<output>/<os>/<arch>/`.
- `--all-arch` exports every platform found in the OCI index (directory output required).

### Push
```
ds porter push <binary> <ref>
ds porter push --manifest=ds.manifest.yaml <ref>
```
Single binaries are pushed directly. Multi-architecture releases rely on a manifest (see `examples/` in the DS repo) that maps platform triplets to build artifacts. The manifest path may be relative to the project root.

### List
```
ds porter list | jq
```
Returns cached artifact metadata, including the registry reference and digest used by DS for subsequent operations.

### Execute Another Plugin
```
ds porter execute-plugin <artifact-id> <plugin> [args...]
```
Primarily used by DS; Porter records the request and yields control back to the host for the actual invocation.

## Configuration

Porter consumes DS configuration via environment variables supplied by the host. The most notable keys are:

| Variable | Description |
| --- | --- |
| `DS_CACHE_DIR` | Base cache directory (`<dir>/porter` is used internally). |
| `DS_AUTH_CREDENTIALS` | JSON array of registry credentials (`registry`, `username`, `password`/`token`). |
| `DS_REGISTRY_MIRRORS` | JSON array of mirror endpoints. |
| `DS_REGISTRY_INSECURE` | JSON array of registries that may be accessed over HTTP. |
| `DS_LOGGING_LEVEL` | Sets the hclog log level (default `info`). |

When running standalone you can export these variables manually or rely on the defaults baked into the binary.

## Build & Release

Requires Go 1.25 or newer on your build host.

- `make build` – produce `bin/ds-porter` for the current platform.
- `make release-build-all VERSION=v0.2.0` – generate the multi-platform tree consumed by `ds.manifest.yaml`.
- `make clean` – remove build outputs.

The resulting binaries are intended to be loaded by Delivery Station; direct execution beyond the standard `--version`/`--help` flags is not supported.

The release artifacts are arranged as:
```
bin/
├── darwin/amd64/ds-porter
├── darwin/arm64/ds-porter
├── linux/amd64/ds-porter
├── linux/arm64/ds-porter
├── windows/amd64/ds-porter.exe
└── windows/arm64/ds-porter.exe
```
Keep the layout in sync with the manifest if you introduce new platforms.

## Repository Layout

```
porter/
├── cmd/porter/        # go-plugin entry point and CLI wiring
├── pkg/porter/        # Artifact pull/export logic and DS integration helpers
├── internal/adapter/  # Optional DS client adapter for higher-level workflows
└── internal/storage/  # Lightweight metadata store used by the adapter
```

## License

MIT
