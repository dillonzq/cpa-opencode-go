# v0.1.1

Patch release for **cpa-opencode-go**, based on v0.1.0 and requiring
**CLIProxyAPI v8.0.0 or later**. Configuration keys, credential records, and
existing `opencode-go/<model>` IDs are unchanged, so an upgrade only replaces
the plugin library.

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
  strip the suffix for catalog routing and apply its reasoning control with CPA's
  suffix priority over the body's own control, dropping the superseded control
  before conversion so an unrepresentable value cannot fail a request the suffix
  already replaced. `none` disables, `auto` defers to upstream defaults, levels
  use the fixed conversion tables, a numeric value keeps its exact Messages
  budget, and an unrecognized value strips the suffix only, matching CPA. Catalog
  IDs that literally contain parentheses still resolve when the stripped name
  does not; CPA labels usage reasoning effort from the client payload, so a
  suffix is not reflected in that label.

## Testing

- Add cross-protocol streaming and non-streaming regressions for reasoning,
  refusal, and tool lifecycles, including refusal completion events emitted
  exactly once, plus suffix priority coverage on every route.

## Installation

Download the ZIP matching the **CPA host's operating system and architecture**
and `checksums.txt` from this release. Extract the library into
`<cliproxyapi_root>/plugins/<os>/<arch>/` and configure
`plugins.configs.cpa-opencode-go` with `enabled: true` and your OpenCode Go key.
The global `plugins.enabled` switch must also be `true`.

Assets are named `cpa-opencode-go_0.1.1_<os>_<arch>.zip`:

| OS | Architectures | Library |
| --- | --- | --- |
| Linux | amd64, arm64 | `cpa-opencode-go.so` |
| macOS (`darwin`) | amd64, arm64 | `cpa-opencode-go.dylib` |
| Windows | amd64, arm64 | `cpa-opencode-go.dll` |
| FreeBSD | amd64 | `cpa-opencode-go.so` |

Each ZIP includes the MIT license with the original and current maintainer's
copyright notices. See the [English installation guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.1.1/README.md#install)
or [中文安装指南](https://github.com/dillonzq/cpa-opencode-go/blob/v0.1.1/README.zh-CN.md#安装).

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
