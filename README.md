# cpa-opencode-go

[English](README.md) | [简体中文](README.zh-CN.md)

A native dynamic Go plugin for [CLIProxyAPI](https://help.router-for.me/plugin/development) that exposes OpenCode Go as a single provider (`opencode-go`).

The plugin unifies model discovery, protocol translation, and execution across OpenCode Go's upstream endpoints while leveraging CLIProxyAPI's built-in authentication, scheduling, keys rotation, and cooldown management.

Independently maintained by [dillonzq](https://github.com/dillonzq), based on [massiveits/opencode-go-cliproxyapi](https://github.com/massiveits/opencode-go-cliproxyapi). The original MIT license and copyright notice are preserved.

The plugin ID is `cpa-opencode-go`. The provider ID, credential type, and default model prefix remain `opencode-go`.

## The Problem

OpenCode Go exposes models across multiple API protocols (OpenAI Chat Completions `/v1/chat/completions`, Anthropic Messages `/v1/messages`, and OpenAI Responses `/v1/responses`).

Without this plugin, using OpenCode Go in CLIProxyAPI requires configuring separate provider blocks for each protocol family. This leads to:
- **Duplicated configuration & keys**: The same API keys must be configured across multiple provider blocks.
- **Fragmented scheduling & rotation**: Keys rotation, rate limits, and cooldowns cannot be shared across protocols—exhausting quota on one protocol does not coordinate with another.
- **Client protocol burden**: Clients must know beforehand which upstream protocol and endpoint each model requires.
- **Fragmented catalog**: Models are split across disjoint provider namespaces instead of a unified model list.

## The Solution

This plugin exposes OpenCode Go as a single provider (`opencode-go`) backed by a shared keys pool:
- **Unified auth pool**: Configure keys once; CLIProxyAPI schedules, rotates, and cools down keys across all protocols.
- **Transparent protocol translation & routing**: Clients request models (e.g. `opencode-go/glm-5.2`, `opencode-go/gpt-5.6-luna`) without needing to know the upstream protocol format.
- **Single model catalog**: All models are discovered and published under the `opencode-go` provider namespace in `/v1/models`.

## Features

- **Single Provider Namespace**: Exposes models under the `opencode-go` provider prefix (e.g. `opencode-go/glm-5.2`, `opencode-go/qwen3.7-max`, `opencode-go/gpt-5.6-luna`).
- **Multi-Protocol Translation**: Translates requests and streaming responses between client formats and upstream endpoints:
  - OpenAI Chat Completions (`/v1/chat/completions`)
  - Anthropic Messages (`/v1/messages`)
  - OpenAI Responses (`/v1/responses`)
- **Thinking & Reasoning Support**: Preserves native reasoning controls and uses fixed effort/budget conversion across formats. Capability metadata does not filter or clamp requests; upstream validates controls. Preserves explicit off and Claude adaptive effort; cross-format auto uses target defaults where no wire equivalent exists. Controls that cannot be converted fail explicitly.
- **Dynamic Catalog Discovery**: Fetches remote model catalogs with local fallback and custom route overrides.
- **Multi-Key Auth Scheduling**: Pools multiple API keys with CLIProxyAPI's native scheduler for rotation, retries, and error cooldowns across all protocols.
- **Native quotas**: Rolling, weekly, and monthly quotas use CLIProxyAPI's generic quota endpoints and the selected credential. The separate plugin page is removed.

## Requirements

- **CLIProxyAPI**: `v8.0.0+`
- **Go Toolchain (source builds only)**: Go 1.26.7+ (with CGO enabled for C-shared build mode)

## Request behavior

- Cross-protocol regular and streaming responses preserve visible reasoning as `reasoning_content`, `thinking`, or Responses reasoning summaries. See the [protocol conversion guide](docs/protocol-conversion.md) for mappings and fields that are omitted.
- Execute CLIProxyAPI's effective `Payload` in `SourceFormat`, including interceptor changes. Only omitted/null payloads fall back to `OriginalRequest`; an explicit empty payload is validated as empty input; `Format` selects the output protocol.
- Honor CLIProxyAPI model-name thinking suffixes (`opencode-go/glm-5.2(high)`): the suffix is stripped for catalog routing and its reasoning control is applied to the upstream request with priority over the body's own control. Unrecognized values strip the suffix only, matching CPA. See the [protocol conversion guide](docs/protocol-conversion.md) for the per-protocol mapping.
- Each upstream HTTP call owns a host operation and its request callback scope. Client disconnection, scope closure, `request-timeout`, and stopped catalog refreshes cancel actual HTTP work. The timeout covers stream opening and consumption together.
- `plugin.quiesce` rejects new work, cancels active tasks, and drains callbacks. Shutdown waits for every callback to exit before the shared library can unload; a host callback that never returns can therefore delay shutdown indefinitely.
- EOF before a protocol terminal state reports a stream error instead of success, including partial tool arguments. A known `finish_reason`/`stop_reason` may still end without `[DONE]`/`message_stop`; final SSE data without a trailing separator is processed, and incomplete terminal payloads report an error. Messages requires `type: message_stop`; Responses requires the matching event type and a response with a nonempty `id`, `object: response`, matching terminal `status`, and an `output` array.
- Session identity uses `canonical_session_id`, then explicit session headers, then the initial user content of the effective input. Existing text/image/tool-result hashes stay compatible. Files, image file references, and unknown native content use stable JSON hashing, without translation validation or content logging. An interceptor changing the initial user content also changes the fallback hash; explicit identity takes precedence.

## Install

Download the ZIP for your version and `checksums.txt` from [GitHub Releases](https://github.com/dillonzq/cpa-opencode-go/releases). The first independent release is `v0.1.0`; draft releases are visible only to maintainers.

Choose the **CPA host's operating system and architecture**, using the environment inside the container for container deployments:

| OS | Architecture | ZIP suffix | Library |
|---|---|---|---|
| Linux | amd64 / arm64 | `linux_amd64.zip` / `linux_arm64.zip` | `cpa-opencode-go.so` |
| macOS | amd64 / arm64 | `darwin_amd64.zip` / `darwin_arm64.zip` | `cpa-opencode-go.dylib` |
| Windows | amd64 / arm64 | `windows_amd64.zip` / `windows_arm64.zip` | `cpa-opencode-go.dll` |
| FreeBSD | amd64 | `freebsd_amd64.zip` | `cpa-opencode-go.so` |

For example: `cpa-opencode-go_0.1.0_linux_amd64.zip`. Each ZIP contains the shared library and `LICENSE`. Prebuilt libraries do not require the Go toolchain.

1. Verify the download: use `sha256sum cpa-opencode-go_0.1.0_linux_amd64.zip` on Linux, `shasum -a 256 <archive>` on macOS, or `Get-FileHash <archive> -Algorithm SHA256` in Windows PowerShell. Compare the hash with the matching entry in `checksums.txt`.
2. Stop CPA. Before upgrading, back up `config.yaml`, the credential directory specified by `auth-dir`, and the existing plugin libraries.
3. Extract the ZIP and place the library in `<cliproxyapi_root>/plugins/<os>/<arch>/`, such as `plugins/linux/amd64/`. Retain the included license.
4. Follow [Configuration](#configuration) to enable both `plugins.enabled` and `plugins.configs.cpa-opencode-go.enabled` and set an API key. Existing users should first follow [Migration](#migrating-from-opencode-go-cliproxyapi).
5. Restart CPA and check its logs for successful loading and duplicate provider errors. Use `GET /v1/models` with a CPA client key to verify discovery, then make a real request. Quota endpoints require the management key; see [Account names and quotas](#account-names-and-quotas).

### Rollback

Stop CPA, remove the new library, and restore the backed-up library, configuration, and credential directory together. Restore the old configuration key and original `store` metadata when returning to the original plugin. Ensure only one plugin is installed, then restart and verify model discovery and a request. Validate upgrades in an isolated environment before switching production traffic.

## Build

Build the shared library with the `debug` tag for local development.
CI handles release builds.

### Windows (AMD64)
```powershell
go build -tags debug -buildmode=c-shared -o plugins/windows/amd64/cpa-opencode-go.dll .
```

### Linux (AMD64)
```bash
go build -tags debug -buildmode=c-shared -o plugins/linux/amd64/cpa-opencode-go.so .
```

### macOS (ARM64)
```bash
go build -tags debug -buildmode=c-shared -o plugins/darwin/arm64/cpa-opencode-go.dylib .
```

Place the compiled binary into your CLIProxyAPI plugin directory (e.g. `<cliproxyapi_root>/plugins/<os>/<arch>/`).

Release builds inject the Git tag version into plugin metadata. Local builds report `0.0.0-dev` unless built with `-ldflags "-X github.com/dillonzq/cpa-opencode-go/internal/plugin.pluginVersion=<version>"`.

### Migrating from opencode-go-cliproxyapi

1. Stop CLIProxyAPI.
2. Rename `plugins.configs.opencode-go-cliproxyapi` to `plugins.configs.cpa-opencode-go`, keeping the plugin settings and setting `enabled: true`.
3. Remove all old `opencode-go-cliproxyapi` shared libraries, including versioned filenames such as `opencode-go-cliproxyapi-v0.1.10.dylib`, and install the new `cpa-opencode-go` library for your platform. Do not enable both plugins: they register the same provider.
4. Restart CLIProxyAPI and hard-refresh Management Center. Quota requests use the native endpoints described in [Account names and quotas](#account-names-and-quotas). CLIProxyAPI v8.0.0 or later and a dashboard supporting the generic quota API are required.

Existing `opencode-go/<model>` IDs, API keys, and credential records continue to work.

If the old plugin was installed through Plugin Store, its configuration may contain a `store` block with the old repository and a pinned version. For manual installation, remove this block from the renamed configuration; otherwise CPA may skip the new binary because its filename does not match the old version pin. For a store-managed installation, install a new store entry for `cpa-opencode-go` pointing to `dillonzq/cpa-opencode-go` instead of copying the old `store` metadata.

## Configuration

Configure the plugin in your CLIProxyAPI `config.yaml` under `plugins.configs.cpa-opencode-go`:

```yaml
plugins:
  enabled: true
  configs:
    cpa-opencode-go:
      enabled: true
      # Upstream base URL (default: "https://opencode.ai/zen/go/v1")
      base-url: "https://opencode.ai/zen/go/v1"

      # Optional catalog endpoint override (default: "{base-url}/models")
      # catalog-url: "https://opencode.ai/zen/go/v1/models"

      # Client-facing model ID prefix configuration
      model-prefix:
        enabled: true           # true -> "opencode-go/<model>", false -> bare "<model>" (default: true)
        value: "opencode-go"    # prefix name (default: "opencode-go")

      # OpenCode Go API keys (at least one required). Supports ${ENV_VAR} expansion.
      api-keys:
        - value: "sk-opencode-key-1"
          name: "Personal"      # optional account label
        - value: "sk-opencode-key-2"
        - value: "${OPENCODE_GO_API_KEY}"

      # Catalog discovery settings
      catalog:
        refresh-interval: "15m"          # discovery refresh cadence, min "1m" (default: "15m")
        stale-while-unavailable: true    # retain last good catalog snapshot on refresh failure (default: true)

      # Protocol enable/disable switches (all default to true)
      protocols:
        chat-completions: true   # enables models routed to /v1/chat/completions
        messages: true           # enables models routed to /v1/messages
        responses: true          # enables models routed to /v1/responses

      # Explicit route overrides per model (takes priority over built-in prefix routing)
      route-overrides:
        "custom-model":
          protocol: "messages"           # "chat-completions" | "messages" | "responses"
          endpoint: "/v1/messages"       # must start with /

      # Execution settings
      request-timeout: "5m"              # upstream request timeout (default: "5m")
      max-response-bytes: 67108864       # max non-streaming response body size in bytes (default: 64 MiB)
      allow-http: false                  # allow http:// scheme for local mock/testing (default: false)
```

### Configuration Options

| Option | Type | Default | Description |
|---|---|---|---|
| `api-keys` | `[]object` | *(Required)* | List of API keys (`- value: "..."`, optional `name: "Personal"`). Supports `${ENV_VAR}` expansion. Duplicates and empty values are rejected. |
| `base-url` | `string` | `https://opencode.ai/zen/go/v1` | Upstream base URL. Must be valid HTTPS (or HTTP if `allow-http: true`) without query parameters, fragments, or userinfo. |
| `catalog-url` | `string` | `{base-url}/models` | Full URL for catalog discovery. Defaults to `{base-url}/models`. |
| `model-prefix.enabled` | `bool` | `true` | When `true`, client-facing model names use `<prefix>/<model>`. When `false`, uses bare model IDs. |
| `model-prefix.value` | `string` | `opencode-go` | Provider prefix string when prefixing is enabled. |
| `catalog.refresh-interval` | `duration` | `15m` | Interval between catalog polling refreshes (e.g. `15m`, `1h`). Minimum is `1m`. |
| `catalog.stale-while-unavailable` | `bool` | `true` | When `true`, serves the last valid catalog snapshot if an update fails. |
| `protocols.chat-completions` | `bool` | `true` | Protocol switch for Chat Completions endpoints. |
| `protocols.messages` | `bool` | `true` | Protocol switch for Messages endpoints. |
| `protocols.responses` | `bool` | `true` | Protocol switch for Responses endpoints. |
| `route-overrides` | `map` | `{}` | Map of model ID to `{ protocol: "...", endpoint: "..." }` overriding built-in family routing. Valid protocols: `chat-completions`, `messages`, `responses`. |
| `request-timeout` | `duration` | `5m` | Deadline for upstream HTTP opening and the entire stream; expiry cancels the host operation. Must be positive. |
| `max-response-bytes` | `int64` | `67108864` (64 MiB) | Maximum non-streaming response body size in bytes. |
| `allow-http` | `bool` | `false` | When `true`, permits `http://` scheme in `base-url` / `catalog-url` for local testing. |

### Catalog capability extensions

`catalog-url` accepts an OpenAI-style `{"data":[{"id":"..."}]}` list. Optional metadata fields are `display_name`, `description`, `context_window`, `max_tokens`, `input_modalities`, `output_modalities`, and `supported_reasoning_levels` (objects with `effort` and optional `description`). Reasoning efforts are published as host `Thinking.Levels`; `none` also enables `ZeroAllowed`. Missing reasoning metadata stays unknown; an empty array explicitly publishes no reasoning levels. Legacy `context_length`, `max_output_tokens`, `input_modes`, `output_modes`, and `thinking` catalog extensions are ignored. This remains a `data`/`id` catalog, not a full Codex `models`/`slug` response.

Metadata is merged field by field with priority **user `models` configuration > catalog > models.dev**. Explicit zero, false, and empty arrays override lower-priority data, including individual `thinking` fields. `models[].name` is the exact upstream ID: entries already in the catalog override metadata; entries missing from the catalog declare additional models. New models use the existing family routes or an explicit `route-overrides` entry. Protocol switches, endpoint checks, and ID collision checks apply to both sources. Explicit declarations remain available when the catalog is empty or unavailable, including under `stale-while-unavailable: false`; removing a declaration removes it unless the upstream catalog still lists it. Models.dev alone never adds models. Provider configuration fields match CPA: `display-name`, `max-context-length`, `input-modalities`, `output-modalities`, and `thinking` (`min`, `max`, `zero-allowed`, `dynamic-allowed`, `levels`). `description` and `max-tokens` are plugin extensions.

Add these settings under the plugin configuration:

```yaml
models:
  - name: glm-5.3
    display-name: My GLM
    max-context-length: 200000
    max-tokens: 64000
    input-modalities: [text]
    output-modalities: [text]
    thinking:
      levels: [none, low, medium, high]
      zero-allowed: true
models-dev:
  enabled: true                     # default
  url: https://models.dev/api.json   # default
  refresh-interval: 24h              # default; minimum 1m
```

For a new ID outside known model families, add its declaration and route:

```yaml
models:
  - name: custom-model
    display-name: Custom model
    max-context-length: 200000
route-overrides:
  custom-model:
    protocol: responses
    endpoint: /v1/responses
```

Declared models are published with host `UserDefined: true` when absent from the upstream catalog, using the usual configured public ID prefix. Execution sends the original `name` to the configured upstream; the declaration does not change upstream availability or add aliases.

The fallback matches only `opencode-go.models[upstreamID]`, with no cross-provider guesses. It uses host HTTP transport without the upstream API key, a 3-second deadline, and a 16 MiB response limit. Successful snapshots are cached in memory for the configured interval and carried across reconfiguration when the source URL stays the same. Fetch failures keep the previous fallback snapshot, emit a warning, and retry on later catalog refreshes; they do not fail model discovery. A source that advertises reasoning without effort levels does not produce invented effort levels; budget/toggle controls are preserved in host thinking metadata. All merged supported fields are returned by both `model.static` and `model.for_auth`, including description, limits, modalities, and thinking. Price data is not included.

CPA v8.0.0's ordinary `/v1/models` response strips capability extensions. Its Codex catalog uses host templates for some known models: those templates may retain their context window because the plugin SDK does not expose the separate `MaxContextLength` override. The merged context still reaches the host registry as `ContextLength`; client output remains subject to the host's catalog generation rules.

## Testing

```powershell
# Run all tests
go test -tags debug ./...

# Verify release archives include the library and license
go test ./.github/scripts

# Run tests with coverage
go test -tags debug ./... -cover

# Run linter / vetting
go vet -tags debug ./... ./.github/scripts
```

## Account names and quotas

Set an optional `name` on each `api-keys` entry, alongside `value`.
For example, use `name: Personal` or `name: Work`. Without a name, one key
uses **OpenCode Go**; multiple keys use **OpenCode Go 1**, **OpenCode Go 2**, and subsequent numbers.
Default numbers follow configuration order. Explicit names remain stable when keys are reordered.
The usage response contains no email or account identity. The plugin does not infer an email from a key.

New credential files have readable names. Existing auth IDs remain unchanged, and
only legacy labels with the exact old prefix (`OpenCode Go credential ` or
`opencode-go-key-`) followed by a 64-character hexadecimal digest are replaced when parsed.
Other custom labels are preserved unless configuration specifies a name. Existing filenames are retained automatically;
stop CLIProxyAPI before renaming an old file to a readable `.json` filename, and keep
its JSON `id` and all other fields unchanged. Back up the file first.

New filenames avoid case-insensitive collisions by appending a number (for example,
`Personal-2.json` when `personal.json` exists). Windows reserved device names receive an
`OpenCode-Go-` prefix (for example, `CON` produces `OpenCode-Go-CON.json`). These rules
apply on every platform and affect filenames only, preserving the configured account label.

Discover support with `GET /v0/management/quota/providers`, then call
`POST /v0/management/quota/fetch` with `{"auth_index":"<selected index>"}`.
These endpoints require the CLIProxyAPI management key. The normalized response contains
`subscription.plan`, `groups[].buckets[].window`, `remainingFraction`, and `resetTime`.
Missing windows are omitted. Invalid readings return an error, not an invented zero.
Quota reset is unsupported. The old plugin quota route and resource page are removed.
