# v0.3.3

## Fixes

- Default upstream paths now match the official Go and Go Plus gateway:
  `/openai/v1/chat/completions`, `/openai/v1/responses`, and
  `/anthropic/v1/messages`. Gemini and model catalog paths are unchanged.
  Explicit global and per-model endpoint overrides still take precedence;
  update configured legacy paths to include the provider prefix when using
  the official gateway.
- All outbound HTTP requests now send `User-Agent: cpa-opencode-go/<version>`
  using the plugin build version, identifying the client for Go and Go Plus.
  This includes regular and streaming inference, catalog and metadata
  refreshes, and quota queries, without extra configuration.

# v0.3.2

Patch release for **cpa-opencode-go**, based on v0.3.1 and requiring
**CLIProxyAPI v8.0.0 or later**. Configuration, credential records, and existing
`opencode-go/<model>` IDs remain compatible with v0.3.1.

## Fixes

- DeepSeek models now publish the ability to disable thinking, including `none`
  in known non-empty effort lists. This prevents newer CPA hosts from changing
  `none` to the lowest enabled effort before the plugin can apply the v0.3.1
  fix. Catalog and models.dev metadata retain their enabled effort levels;
  explicit user thinking overrides keep priority. No manual model capability
  overrides are required.

## Installation and upgrade

Download `cpa-opencode-go_0.3.2_<os>_<arch>.zip` for the **CPA host's operating
system and architecture** and `checksums.txt` from this release. Verify the ZIP's
SHA256, stop CPA, and back up its configuration, credential directory, and
existing plugin library. Replace the library in
`<cliproxyapi_root>/plugins/<os>/<arch>/`, retaining the included `LICENSE`.
Restart CPA, verify `/v1/models` with a CPA client key, and make a request.

# v0.3.1

Patch release for **cpa-opencode-go**, based on v0.3.0 and requiring
**CLIProxyAPI v8.0.0 or later**. Configuration, credential records, and existing
`opencode-go/<model>` IDs remain compatible with v0.3.0.

## Fixes

- DeepSeek Chat Completions and Messages requests now render reasoning effort
  `none` as `thinking.type: "disabled"`, removing the corresponding effort
  field. This covers body controls, cross-protocol requests, and `(none)` / `(0)`
  suffixes in regular and streaming execution. Suffix priority is preserved;
  Responses and other model families are unchanged.

## Installation and upgrade

Download `cpa-opencode-go_0.3.1_<os>_<arch>.zip` for the **CPA host's operating
system and architecture** and `checksums.txt` from this release. Verify the ZIP's
SHA256, stop CPA, and back up its configuration, credential directory, and
existing plugin library. Replace the library in
`<cliproxyapi_root>/plugins/<os>/<arch>/`, retaining the included `LICENSE`.
Restart CPA, verify `/v1/models` with a CPA client key, and make a request.

# v0.3.0

Minor release for **cpa-opencode-go**, based on v0.2.2 and requiring
**CLIProxyAPI v8.0.0 or later**. Translation now uses **apix v0.1.0**, replacing
api-translator v0.3.0. Credential records and existing `opencode-go/<model>`
IDs remain compatible. Configuration keys also remain compatible except for the
removed `excluded-models` and `route-overrides` settings, the reworked
`protocols` settings, and the new `base-url` default; see below.

## Translation library

- Replace api-translator v0.3.0 with [apix v0.1.0](https://github.com/dillonzq/apix/tree/v0.1.0)
  for Chat Completions, Messages, and Responses requests, regular responses,
  and streams. Each execution uses one apix exchange, so tool identities from
  request conversion are restored in the response or stream. Native non-GPT
  Responses tool normalization now runs inside that exchange.
- Cross-protocol Responses streams without an upstream response ID use `apix`
  instead of `api-translator` as the fallback ID.

## Protocol routing

- A model can serve several upstream protocols. Each request uses the client's
  own protocol when the model serves it, otherwise the served protocol with the
  least semantic loss in a fixed per-format order. See
  [protocol selection](https://github.com/dillonzq/cpa-opencode-go/blob/v0.3.0/README.md#protocol-selection).
- DeepSeek models serve Chat Completions, Responses, and Messages, so Claude Code
  and Codex requests reach them without conversion.
- Add Gemini GenerateContent as an upstream protocol for `gemini-*` models and as
  a native client format. Gemini requests use `x-goog-api-key`.
- The built-in protocol table follows the current OpenCode Go endpoint table:
  `claude-*`, `step-*`, and `beam-*` models are now routable, and
  `minimax-m3.1-flash*` uses Chat Completions instead of Messages.
- Each model has a switch per protocol, with defaults from the built-in table.
  `models[].protocols` switches one model's protocols on or off and overrides
  their endpoints; the top-level `protocols` sets endpoint paths for every
  model. See
  [protocol configuration](https://github.com/dillonzq/cpa-opencode-go/blob/v0.3.0/README.md#protocol-configuration).

## Behavior changes

- Reasoning sent to the Messages route now uses adaptive thinking:
  `thinking.type` is `"adaptive"` and the level goes in `output_config.effort`,
  instead of `"enabled"` with a fixed `budget_tokens`. This applies to reasoning
  converted from Chat Completions or Responses and to model-name suffixes.
  Native Messages thinking controls without a suffix pass through unchanged.
- Numeric suffixes on Messages, such as `opencode-go/minimax-m3(16384)`, map to
  an effort by fixed thresholds instead of keeping the exact budget. Chat
  Completions and Responses mappings are unchanged.
- Remove the plugin's `excluded-models` setting in favor of CLIProxyAPI's native
  OAuth provider exclusions. Move patterns to `oauth.excluded-models` under the
  `opencode-go` provider key. CLIProxyAPI matches client-facing model IDs with
  the same case-insensitive `*` wildcards, so include the model prefix when it
  is enabled, such as `opencode-go/glm-5.2` or `opencode-go/gpt-*`. The plugin
  ignores a leftover `excluded-models` key, so those models become visible
  again until the host setting is configured.
- The default `base-url` is now `https://opencode.ai/inference/go`, the address
  in the current OpenCode Go documentation, and `base-url` no longer includes a
  version segment: endpoint paths carry `/v1` or `/v1beta`. A trailing `/v1` is
  dropped, so a `base-url` written for v0.2.2 keeps working. The default catalog
  is `{base-url}/v1/models`. Gemini models need the inference address.
- Move protocol switches from the top level to each model. The top-level
  `protocols` now accepts only endpoints, so a leftover switch such as
  `protocols.gemini: false` fails configuration loading. Set
  `gemini: {enabled: false}` under `models[].protocols` for each model instead;
  model entries take an object, not a bare boolean.
- Remove `route-overrides` in favor of `models[].protocols`. A leftover key is
  ignored, so move each entry into the model's declaration. A declaration now
  adds to the model's default protocols instead of replacing them; switch the
  others off to keep only one:

  ```yaml
  models:
    - name: custom-model
      protocols:
        responses:
          endpoint: /v1/responses   # optional
  ```

## Compatibility

Source builds now require **Go 1.26.10+**. Prebuilt libraries do not need a Go
toolchain. DeepSeek on Responses and Messages, the Gemini endpoint, and adaptive
thinking on OpenCode Go Messages models are verified by offline tests only; if an
upstream rejects `thinking.type: "adaptive"` or `output_config.effort`, roll back
to v0.2.2.

## Installation and upgrade

Download the ZIP matching the **CPA host's operating system and architecture**
and `checksums.txt` from this release. Verify its SHA256, stop CPA, and back up
its configuration, credential directory, and existing plugin library. Extract
`cpa-opencode-go_0.3.0_<os>_<arch>.zip` into
`<cliproxyapi_root>/plugins/<os>/<arch>/`, retaining the included `LICENSE`.

| OS | Architectures | Library |
| --- | --- | --- |
| Linux | amd64, arm64 | `cpa-opencode-go.so` |
| macOS (`darwin`) | amd64, arm64 | `cpa-opencode-go.dylib` |
| Windows | amd64, arm64 | `cpa-opencode-go.dll` |
| FreeBSD | amd64 | `cpa-opencode-go.so` |

Both `plugins.enabled` and `plugins.configs.cpa-opencode-go.enabled` must be
`true`. Restart CPA, verify `/v1/models` with a CPA client key, and make a request.
For a new installation or migration from `opencode-go-cliproxyapi`, follow the
[English installation guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.3.0/README.md#install)
or [中文安装指南](https://github.com/dillonzq/cpa-opencode-go/blob/v0.3.0/README.zh-CN.md#安装).

## Known limitations and rollback

The [protocol conversion guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.3.0/docs/protocol-conversion.md)
records supported mappings and remaining compatibility gaps; see the
[apix LLM protocol guide](https://github.com/dillonzq/apix/blob/v0.1.0/docs/protocols/llm.md)
for field-level limits. Target-model and protocol limitations from v0.2.2 still
apply. Offline ABI tests and platform builds do not replace validation with a
running CPA host and real upstream credentials.

To roll back, stop CPA and restore the backed-up plugin library, configuration,
and credential directory together. Restart CPA and verify model discovery and a
request. Keep only one plugin registering the `opencode-go` provider installed.

# v0.2.2

Patch release for **cpa-opencode-go**, based on v0.2.1 and requiring
**CLIProxyAPI v8.0.0 or later**. Configuration keys, credential records, and
existing `opencode-go/<model>` IDs remain compatible, so upgrading only replaces
the plugin library. Translation remains on **api-translator v0.3.0**.

## Fixes

- Normalize DeepSeek image `detail: "original"` to the equivalent `"high"`
  before sending Chat Completions or Responses requests through OpenCode Go.
  Covers historical messages and tool output images in regular and streaming
  requests, preventing repeated gateway validation errors after viewing images.
  Other models, detail values, tool schemas, and argument strings are unchanged.

## Installation and upgrade

Download the ZIP matching the **CPA host's operating system and architecture**
and `checksums.txt` from this release. Verify its SHA256, stop CPA, and back up
its configuration, credential directory, and existing plugin library. Extract
`cpa-opencode-go_0.2.2_<os>_<arch>.zip` into
`<cliproxyapi_root>/plugins/<os>/<arch>/`, retaining the included `LICENSE`.

| OS | Architectures | Library |
| --- | --- | --- |
| Linux | amd64, arm64 | `cpa-opencode-go.so` |
| macOS (`darwin`) | amd64, arm64 | `cpa-opencode-go.dylib` |
| Windows | amd64, arm64 | `cpa-opencode-go.dll` |
| FreeBSD | amd64 | `cpa-opencode-go.so` |

Both `plugins.enabled` and `plugins.configs.cpa-opencode-go.enabled` must be
`true`. Restart CPA, verify `/v1/models` with a CPA client key, and make a request.
For a new installation or migration from `opencode-go-cliproxyapi`, follow the
[English installation guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.2.2/README.md#install)
or [中文安装指南](https://github.com/dillonzq/cpa-opencode-go/blob/v0.2.2/README.zh-CN.md#安装).

## Known limitations and rollback

The [protocol conversion guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.2.2/docs/protocol-conversion.md)
records supported mappings and remaining compatibility gaps. Target-model and
protocol limitations from v0.2.1 still apply. Offline ABI tests and platform
builds do not replace validation with a running CPA host and real upstream
credentials.

To roll back, stop CPA and restore the backed-up plugin library, configuration,
and credential directory together. Restart CPA and verify model discovery and a
request. Keep only one plugin registering the `opencode-go` provider installed.

# v0.2.1

Patch release for **cpa-opencode-go**, based on v0.2.0 and requiring
**CLIProxyAPI v8.0.0 or later**. Translation now uses **api-translator v0.3.0**.
Configuration keys, credential records, and existing `opencode-go/<model>` IDs
remain compatible, so upgrading only replaces the plugin library.

## Model configuration

- Add `excluded-models` for exact and case-insensitive `*` wildcard exclusions
  against upstream model IDs. The blacklist filters discovered and declared
  models from listing and execution, takes priority over route overrides, and
  reapplies to cached catalogs during reconfiguration and upstream outages.

## Fixes

- Preserve original reasoning content and summaries in native non-GPT Responses
  request history, including DeepSeek tool-call continuations. Cleanup now removes
  foreign encrypted state and compaction without deleting readable reasoning.
  Summary-only history is preserved without inventing missing original content.
  Reasoning without non-whitespace `reasoning_text` content or `summary_text`
  summaries is removed, including null/scalar parts, wrong part types, and empty
  or whitespace-only text. Retained items preserve their original fields.
- Preserve original reasoning across protocol requests with assistant text,
  parallel tool calls, and separate tool rounds. Synthesized ordinary Responses
  output, streamed completed items, and final output retain replayable
  `reasoning_text` content alongside display summaries.
- Keep completed stream events emitted before a conversion error, then fail the
  stream without a successful completion. Stricter malformed-JSON validation
  rejects shapes that previously lost fields silently.

## Protocol capabilities

- Map representable file inputs across Chat Completions, Responses, and Messages;
  audio/video inputs map between Chat and Responses. Messages supports PDF,
  UTF-8 plain text, and URL documents; unsupported media fails explicitly.
- Adapt Responses local shell declarations, choice, history, and results to
  function tools. Non-GPT native Responses normalization enables the same path;
  ordinary and streaming Responses output restores shell calls using the
  execution's shared state. The plugin does not execute commands.
- Retain representable message, block, and tool `cache_control` hints when
  converting Chat/Responses to Messages, including TTL and extension fields.
  No cache breakpoints are added automatically.

## Compatibility

Original reasoning cannot be recovered from summaries. Chat/Responses requests
converted to Messages omit unsigned thinking history by default; native Messages
history and response display are preserved. The plugin keeps the library's
`AllowUnsignedThinking` option disabled. Summary-only, opaque, or unsigned
reasoning losses are collected under the existing allow loss policy. Existing
configuration remains compatible; CLIProxyAPI/Go requirements are unchanged.
See the protocol guides for direction-specific media, shell, cache, and
reasoning limits.

## Installation and upgrade

Download the ZIP matching the **CPA host's operating system and architecture**
and `checksums.txt` from this release. Verify its SHA256, stop CPA, and back up
its configuration, credential directory, and existing plugin library. Extract
`cpa-opencode-go_0.2.1_<os>_<arch>.zip` into
`<cliproxyapi_root>/plugins/<os>/<arch>/`, retaining the included `LICENSE`.

| OS | Architectures | Library |
| --- | --- | --- |
| Linux | amd64, arm64 | `cpa-opencode-go.so` |
| macOS (`darwin`) | amd64, arm64 | `cpa-opencode-go.dylib` |
| Windows | amd64, arm64 | `cpa-opencode-go.dll` |
| FreeBSD | amd64 | `cpa-opencode-go.so` |

Both `plugins.enabled` and `plugins.configs.cpa-opencode-go.enabled` must be
`true`. Restart CPA, verify `/v1/models` with a CPA client key, and make a request.
For a new installation or migration from `opencode-go-cliproxyapi`, follow the
[English installation guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.2.1/README.md#install)
or [中文安装指南](https://github.com/dillonzq/cpa-opencode-go/blob/v0.2.1/README.zh-CN.md#安装).

## Known limitations and rollback

The [protocol conversion guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.2.1/docs/protocol-conversion.md)
records supported mappings and remaining compatibility gaps. Target models may
reject media, local shell, cache, or structured output even when their protocol
can represent them. Cross-protocol opaque reasoning, audio output, and
image-generation output remain limited. Quota reset remains unsupported, and the
dashboard must support CPA's generic quota API. Offline ABI tests and platform
builds do not replace validation with a running CPA host and real upstream
credentials.

To roll back, stop CPA and restore the backed-up plugin library, configuration,
and credential directory together. Restart CPA and verify model discovery and a
request. Keep only one plugin registering the `opencode-go` provider installed.

# v0.2.0

Minor release for **cpa-opencode-go**, based on v0.1.2 and requiring
**CLIProxyAPI v8.0.0 or later**. Requests, regular responses, and SSE across
Chat Completions, Messages, and Responses now use **api-translator v0.2.0**.
Configuration keys, credential records, and existing `opencode-go/<model>` IDs
are unchanged. Existing v0.1.2 installations can upgrade by replacing the plugin
library. Native OpenCode Go request cleaning, model suffix precedence, sessions,
authentication, and CPA transport lifecycle are preserved.

## Protocol conversion

- Support representable structured output, tool strict, URL citations, hosted
  search between Responses and Messages, and cross-protocol custom apply_patch.
  Actual target-model support remains upstream-dependent.

## Fixes

- Restore namespace/custom tool identities in native non-GPT Responses replies;
  reject identity collisions. Keep Chat streams compatible with CPA's framing
  contract without duplicate DONE markers.
- Handle multi-line SSE and preserve completed events returned alongside a
  conversion error. Unrepresentable output and failed/non-terminal Responses
  responses fail explicitly rather than being treated as successful conversion.
- Keep stream errors with empty or whitespace-only upstream messages as errors;
  supply a fallback message so CPA does not mistake error closure for success.

## Installation and upgrade

Download the ZIP matching the **CPA host's operating system and architecture**
and `checksums.txt` from this release. Verify its SHA256, stop CPA, and back up
its configuration, credential directory, and existing plugin library. Extract
`cpa-opencode-go_0.2.0_<os>_<arch>.zip` into
`<cliproxyapi_root>/plugins/<os>/<arch>/`, retaining the included `LICENSE`.

| OS | Architectures | Library |
| --- | --- | --- |
| Linux | amd64, arm64 | `cpa-opencode-go.so` |
| macOS (`darwin`) | amd64, arm64 | `cpa-opencode-go.dylib` |
| Windows | amd64, arm64 | `cpa-opencode-go.dll` |
| FreeBSD | amd64 | `cpa-opencode-go.so` |

Both `plugins.enabled` and `plugins.configs.cpa-opencode-go.enabled` must be
`true`. Restart CPA, verify `/v1/models` with a CPA client key, and make a request.
For a new installation or migration from `opencode-go-cliproxyapi`, follow the
[English installation guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.2.0/README.md#install)
or [中文安装指南](https://github.com/dillonzq/cpa-opencode-go/blob/v0.2.0/README.zh-CN.md#安装).

## Known limitations and rollback

The [protocol conversion guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.2.0/docs/protocol-conversion.md)
records supported mappings and remaining compatibility gaps. Target models may
reject structured output or hosted tools even when their protocol can represent
them. Cross-protocol opaque reasoning, audio, and image-generation output remain
limited. Quota reset remains unsupported, and the dashboard must support CPA's
generic quota API. Offline ABI tests and platform builds do not replace
validation with a running CPA host and real upstream credentials.

To roll back, stop CPA and restore the backed-up plugin library, configuration,
and credential directory together. Restart CPA and verify model discovery and a
request. Keep only one plugin registering the `opencode-go` provider installed.

# v0.1.2

Patch release for **cpa-opencode-go**, based on v0.1.1 and requiring
**CLIProxyAPI v8.0.0 or later**. Configuration keys, credential records, and
existing `opencode-go/<model>` IDs are unchanged. Existing v0.1.1 installations
can upgrade by replacing the plugin library.

## Fixes

- Accept images in Responses `function_call_output` and `custom_tool_call_output`
  when converting requests to Chat Completions or Messages. Messages retains
  mixed text/image content inside `tool_result`. Chat Completions keeps parallel
  tool replies together, then emits mixed results as a user message labeled with
  their call IDs, preserving image/text order. Empty messages do not split
  parallel tool replies. Image parts require `image_url`; file-ID-only references
  remain unsupported.

## Installation and upgrade

Download the ZIP matching the **CPA host's operating system and architecture**
and `checksums.txt` from this release. Verify its SHA256, stop CPA, and back up
its configuration, credential directory, and existing plugin library. Extract
`cpa-opencode-go_0.1.2_<os>_<arch>.zip` into
`<cliproxyapi_root>/plugins/<os>/<arch>/`, retaining the included `LICENSE`.

| OS | Architectures | Library |
| --- | --- | --- |
| Linux | amd64, arm64 | `cpa-opencode-go.so` |
| macOS (`darwin`) | amd64, arm64 | `cpa-opencode-go.dylib` |
| Windows | amd64, arm64 | `cpa-opencode-go.dll` |
| FreeBSD | amd64 | `cpa-opencode-go.so` |

Both `plugins.enabled` and `plugins.configs.cpa-opencode-go.enabled` must be
`true`. Restart CPA, verify `/v1/models` with a CPA client key, and make a request.
For a new installation or migration from `opencode-go-cliproxyapi`, follow the
[English installation guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.1.2/README.md#install)
or [中文安装指南](https://github.com/dillonzq/cpa-opencode-go/blob/v0.1.2/README.zh-CN.md#安装).

## Known limitations and rollback

The [protocol conversion guide](https://github.com/dillonzq/cpa-opencode-go/blob/v0.1.2/docs/protocol-conversion.md)
records supported mappings and remaining compatibility gaps. Quota reset remains
unsupported, and the dashboard must support CPA's generic quota API. Offline
ABI tests and platform builds do not replace validation with a running CPA host
and real upstream credentials.

To roll back, stop CPA and restore the backed-up plugin library, configuration,
and credential directory together. Restart CPA and verify model discovery and a
request. Keep only one plugin registering the `opencode-go` provider installed.

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
