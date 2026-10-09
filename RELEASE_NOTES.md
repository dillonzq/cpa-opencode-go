# Unreleased

## Protocol conversion

- Preserve visible reasoning in regular and streaming responses across Chat
  Completions, Messages, and Responses, including compatible provider aliases.
- Retain refusal text and cache-write token details across supported response
  conversions, preserving explicit zero counts and existing token accounting.
- Reconcile Responses text, reasoning, and tool-argument completion snapshots
  without replaying previously emitted prefixes. Buffer early argument fragments and completion
  until both the real call ID and tool name are available, and serialize Messages tool blocks so later
  content cannot close a tool before its arguments finish.
- Document supported mappings, omissions, and remaining compatibility gaps in
  English and Chinese. Provider-specific signatures and encrypted reasoning
  remain native-only; request-side historical reasoning policies are unchanged.

## Request handling

- Honor CLIProxyAPI model-name thinking suffixes (`opencode-go/glm-5.2(high)`):
  strip the suffix for catalog routing and apply its reasoning control
  (`reasoning_effort`, a Messages thinking budget, or `reasoning.effort`) with
  priority over the body's own control. `none` disables, `auto` defers to
  upstream defaults, levels and numeric budgets use the fixed conversion
  tables, and an unrecognized value strips the suffix only, matching CPA.
  Catalog IDs that literally contain parentheses still resolve when the
  stripped name does not.

# v0.1.0

First independent release of **cpa-opencode-go**, based on
[massiveits/opencode-go-cliproxyapi](https://github.com/massiveits/opencode-go-cliproxyapi).
Requires **CLIProxyAPI v8.0.0 or later**. Existing `opencode-go/<model>` IDs and
credential records remain compatible.

## Features

- One `opencode-go` provider and shared API-key pool for OpenAI Chat Completions,
  Anthropic Messages, and OpenAI Responses, with streaming and non-streaming
  protocol conversion.
- Dynamic model discovery, configurable routing, and explicitly declared models
  absent from the upstream catalog. Model metadata merges user configuration,
  catalog data, and optional cached models.dev data in that order.
- Native CPA key rotation, retry/cooldown scheduling, readable account names,
  and rolling, weekly, and monthly quotas for the selected credential.
- Responses tool lifecycle events, namespace translation, and custom tool
  normalization for supported upstream routes.

## Reliability

- Honor the host's effective request payload and preserve supported reasoning
  controls; upstream services validate model capabilities.
- Cancel actual upstream HTTP work on client disconnect, callback scope closure,
  or timeout. Quiesce drains active work before unloading the library.
- Report truncated streams and malformed terminal events as errors instead of
  successful partial responses. Preserve upstream error explanations.
- Preserve existing credential IDs, filenames, and custom labels. Avoid
  case-insensitive filename collisions and Windows reserved device names.

## Installation

Download the ZIP matching the **CPA host's operating system and architecture**
and `checksums.txt` from this release. Extract the library into
`<cliproxyapi_root>/plugins/<os>/<arch>/` and configure
`plugins.configs.cpa-opencode-go` with `enabled: true` and your OpenCode Go key.
The global `plugins.enabled` switch must also be `true`.

Assets are named `cpa-opencode-go_0.1.0_<os>_<arch>.zip`:

| OS | Architectures | Library |
| --- | --- | --- |
| Linux | amd64, arm64 | `cpa-opencode-go.so` |
| macOS (`darwin`) | amd64, arm64 | `cpa-opencode-go.dylib` |
| Windows | amd64, arm64 | `cpa-opencode-go.dll` |
| FreeBSD | amd64 | `cpa-opencode-go.so` |

Each ZIP includes the MIT license with the original and current maintainer's
copyright notices. See the [English installation guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.1.0/README.md#install)
or [中文安装指南](https://github.com/dillonzq/cpa-opencode-go/blob/v0.1.0/README.zh-CN.md#安装).

## Migration from opencode-go-cliproxyapi

1. Stop CPA and back up its configuration, credential directory, and old plugin.
2. Rename `plugins.configs.opencode-go-cliproxyapi` to
   `plugins.configs.cpa-opencode-go`, preserving settings and enabling the plugin.
3. For manual installation, remove the old `store` block. A store-managed install
   requires a new entry pointing to `dillonzq/cpa-opencode-go`.
4. Remove every old plugin library, including versioned filenames, and install
   the new library. Both plugins register the same provider and must not coexist.
5. Restart CPA and refresh the management dashboard. Existing model IDs, keys,
   and credentials can continue to be used.

Quota support is discovered through `GET /v0/management/quota/providers` and
queried through `POST /v0/management/quota/fetch` with the selected `auth_index`.
Both require the management key. The old plugin quota page has been removed.

## Known limitations

- Quota reset is unsupported. The dashboard must support CPA's generic quota API.
- CPA v8.0.0 filters capability extensions from ordinary `/v1/models` output;
  some Codex catalog entries use host templates that can override the displayed
  context window.
- Cross-format reasoning conversion supports fixed effort/budget mappings;
  controls without a supported conversion report an error. Auto uses target
  defaults when there is no corresponding wire field.
- A host callback that never returns can delay shutdown indefinitely.
- Platform builds do not establish live deployment compatibility. Offline tests
  cover the pinned SDK's Unix loader and ABI; release artifacts still require
  acceptance against a running CPA host and real upstream credentials.

## Rollback

Stop CPA, remove the new plugin library, and restore the backed-up plugin,
configuration, and credential directory together. If migrating back to the
original plugin, restore its original configuration key and store metadata.
Restart CPA and verify model discovery and a request. Do not leave both plugins
installed.
