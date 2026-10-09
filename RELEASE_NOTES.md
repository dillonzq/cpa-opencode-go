## Unreleased — executor, lifecycle, and credential fixes

- Use Codex field names for catalog metadata extensions, without legacy aliases. Add CPA-style `models` declarations/metadata overrides and a cached, optional models.dev fallback: user configuration > catalog > models.dev, preserving explicit zero/false/empty values. Match only exact OpenCode Go model IDs and publish merged metadata through both model RPC methods. Add offline real-host ABI coverage of metadata decoding, registry conversion, and Codex catalog generation. Price metadata is excluded.
- Publish configured models absent from the upstream catalog, with normal family or explicit routing and host `UserDefined` metadata. Keep declarations during catalog outages or empty responses, apply the stale policy only to discovered models, and remove deleted declarations without retaining them as stale catalog entries. Models.dev never introduces undeclared models.
- Preserve the fallback cache across reconfiguration of the same source, including during a fallback outage, without seeding upstream models under a fail-closed catalog policy. Apply changed fallback refresh intervals to the carried cache. Document CPA v8.0.0's Codex template context-window limitation.

- Treat thinking capability metadata as descriptive only. Preserve reasoning effort across OpenAI formats, use fixed budget/effort conversion without model filtering or clamping, preserve explicit off and Claude adaptive effort, and leave output limits/sampling controls to upstream validation. Cross-format auto uses target defaults where no wire equivalent exists; other unconvertible controls fail explicitly.
- Keep CLIProxyAPI v8.0.0 as the minimum dependency. Forward per-request `host_callback_id`, open owned HTTP operations, and cancel actual transport on client/scope cancellation, request timeout, and catalog refresh stop. Keep late operation/stream cleanup tracked through completion.
- Implement `plugin.quiesce`: stop admission, cancel active execution/refresh work, and drain handlers, stream pumps, and FFI callbacks. Shutdown no longer returns after 15 seconds over a live callback; an unresponsive host callback can delay unload indefinitely. Cancellation also closes downstream streams to release backpressure.
- Execute the host's effective `Payload` using `SourceFormat`, with an absent-payload fallback for older callers. Honor `Format` for output; avoid replaying interceptor input or converting already-prepared input twice.
- Reject truncated EOF across Chat Completions, Messages, and Responses streams instead of synthesizing success. Preserve known finish/stop reasons without the final marker, process buffered final SSE data, and prevent duplicate terminal events.
- Separate session extraction from translation validation. Preserve existing supported content hashes and identity precedence, hash files/unknown native content stably, and use effective input for fallback identity (interceptor changes to the initial turn can change that fallback hash). Do not log prompts, file content, or credentials.
- Add streaming/non-streaming regressions, cancellation/ownership tests, and offline debug C-shared integration tests using the pinned SDK's actual Unix loader, guarded RPC client, callback scopes, and HTTP bridges. Cover independent client cancellation, scope end, timeout, quiesce with backpressure and active catalog refresh, truncated streams, and unload with active HTTP. Live CPA deployments and real upstreams remain untested.
- Prevent case-insensitive credential filename collisions, including suffixes and other new accounts in the same registration. Preserve existing credential files instead of overwriting them on Windows/macOS.
- Prefix Windows reserved device basenames (`CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`, `LPT1`–`LPT9`) with `OpenCode-Go-` when generating new filenames; account labels remain unchanged.
- Migrate only legacy labels consisting of the exact old prefix plus a 64-character hexadecimal digest. Preserve custom labels such as `OpenCode Go credential Work` and `opencode-go-key-team`; an explicit configured `name` still takes precedence.
- Correct the existing upgrade instructions to use the generic v8 quota endpoints and remove the obsolete plugin session-cache instruction. Add naming/label regressions and real-host offline ABI coverage for credential preservation and repeated registration.

- Keep the native ABI test driver in `tests/native_host_test.go`, resolving the project root through Go module metadata. Store the host integration tests as standard `_test.go` files under `testdata` and load them with a Go overlay, replacing the fixture extension and full SDK source copy.

- Review follow-up: reject missing/partial terminal SSE payloads, including native EOF tails after a known finish, and include native `input_image.file_id` in stable session hashes. Distinguish an explicit empty effective payload from an omitted/null payload so interceptor output cannot replay the original request. Add converter, non-stream/stream execution, and real-host ABI regressions.

- Address PR #3 review: require the `message_stop` discriminator and coherent Responses terminal snapshots (`type`, response `id`, `object`, terminal `status`, and an `output` array). Reject empty/unrelated objects before native passthrough or conversion; cover framed and EOF terminals across all output formats with converter and real-host ABI tests.

## CLIProxyAPI v8

### Features

- Support CLIProxyAPI v8 and its generic plugin quota API.
- Return rolling, weekly, and monthly quotas for the credential selected by CLIProxyAPI.
- Advertise the quota provider as **OpenCode Go**.
- Remove the separate quota page and custom management routes.
- Accept an optional `name` for each API key.
- Use readable account labels and filenames instead of key digests.
- Preserve existing auth IDs, custom labels, filenames, and host metadata.
- Prevent duplicate auth records at cold start when readable filenames exist.
- Reject invalid quota values and redact provider request errors.

### Upgrade

1. Upgrade CLIProxyAPI to v8.0.0 or later.
2. Replace the plugin binary with the new build.
3. Restart CLIProxyAPI.

The generic endpoints are `GET /v0/management/quota/providers` and `POST /v0/management/quota/fetch`.
Both require the management key. Quota reset is unsupported.

The management dashboard must support the generic quota API to show plugin refresh controls.
Older dashboard builds do not show these controls.

Existing credential files keep their names and stable IDs.
Set `name:` on an API key to choose its display label.
New files use readable names.

## What's Changed

`cpa-opencode-go` is now maintained independently at [dillonzq/cpa-opencode-go](https://github.com/dillonzq/cpa-opencode-go), based on [massiveits/opencode-go-cliproxyapi](https://github.com/massiveits/opencode-go-cliproxyapi).

### Project Identity and Releases

- Rename the plugin ID, configuration key, shared libraries, release archives, and quota management paths to `cpa-opencode-go`.
- Use `github.com/dillonzq/cpa-opencode-go` as the Go module path and update plugin metadata and the quota page GitHub link to the independently maintained repository.
- Keep the `opencode-go` provider ID, credential type, and model prefix for compatibility.
- Inject the release tag version into plugin metadata on all build targets; local builds default to `0.0.0-dev`.
- Include the MIT license in release archives, preserving the original copyright notice and adding the current maintainer's notice.
- Validate release archive contents and reject missing licenses in packaging tests explicitly included in CI.
- Run CI on pushes to `main` and align documented Go requirements with `go.mod` (Go 1.26.7+).

### Inherited Adapter Fixes

- Fix missing Responses stream item lifecycle completion events (output_text.done, content_part.done, function_call_arguments.done, output_item.done) prior to response.completed, resolving dropped assistant output and tool calls in OpenAI Codex CLI and strict Responses clients.
- Fix handling of in-history messages with role: "system" across protocol adapters without rejecting them as unsupported roles or forwarding invalid turn roles to upstream providers that require alternating user/assistant turns.
- Fix handling of multi-part content arrays in function_call_output.output under /v1/responses decoding.
- Fix upstream HTTP >= 400 error propagation in streaming and non-streaming requests by reading and extracting the upstream error payload instead of discarding it with generic fallback errors.
- Add two-way Responses tool namespace and additional_tools translation: merge dynamic declarations from input items (type: "additional_tools"), unroll client-side grouping tools (type: "namespace") into qualified wire names for upstream models, and restore original tool names and namespaces across non-streaming output and streaming SSE events for OpenAI Codex CLI and multi-agent harnesses.
- Add Codex custom tool normalization and hosted tool dropping: rewrite generic custom tools (`type: "custom"`, e.g. `exec`) into function tools with default object schemas, and drop client-only/hosted tools (`apply_patch`, `web_search`, `web_search_preview`, `tool_search`, `image_generation`) when routing to Chat Completions or Messages endpoints, resolving Codex CLI failures on models like `space-bunny-free`.
- Add bidirectional support for `custom_tool_call` and `custom_tool_call_output` conversation items and outbound custom tool events: translate inbound custom tool calls and results across Chat Completions and Messages protocols, unwrap arguments into clean raw input, and restore `custom_tool_call` and `response.custom_tool_call_input.done` on outbound non-streaming and streaming responses, resolving Codex tool dispatch aborts on programmatic tools like `exec`.
- Remove local reasoning effort validation across request translators: allow client-declared reasoning effort levels (e.g. `xhigh`, `max`, and unlisted levels) to pass through transparently to upstream endpoints without local gatekeeping in `chatcompletions`, `responses`, and `messages` adapters.
- Normalize tools for non-GPT native Responses models: unroll namespaces, convert custom tools (`exec`) into function tools, and sanitize `web_search` definitions when targeting non-GPT Responses endpoints (e.g. `muse-spark`, `grok`), while preserving transparent passthrough for native GPT models (e.g. `gpt-6-luna`).
- Restore custom tool calls across native Responses-to-Responses streaming and non-streaming adapters, ensure historical function_call arguments default to valid JSON via `shared.DefaultArgs` to resolve Muse Spark errors, drop compaction input items to fix Grok compaction blob decode failures, and expand `UnwrapCustomToolInput` to unwrap arguments, code, cmd, and command fields.
- Drop historical `reasoning` input items for non-GPT Responses upstreams: resolve Grok HTTP 400 "Could not decode the compaction blob" decryption errors on multi-turn conversations caused by pooled account credential mismatches on encrypted reasoning blobs.


## Upgrade Notes

- Stop CLIProxyAPI and rename `plugins.configs.opencode-go-cliproxyapi` to `plugins.configs.cpa-opencode-go`, keeping its plugin settings and setting `enabled: true`. Ensure `plugins.enabled` is also `true`.
- For manual installation, remove any old `store` block from the renamed configuration so the old repository and version pin are not carried over. For a store-managed installation, install a new entry for `cpa-opencode-go` pointing to `dillonzq/cpa-opencode-go`.
- Remove all old `opencode-go-cliproxyapi` shared libraries, including versioned filenames such as `opencode-go-cliproxyapi-v0.1.10.dylib`, before installing the new `cpa-opencode-go` binary. Enabling both registers the same provider twice.
- Keep existing `opencode-go/<model>` IDs and credential records.
- Restart CLIProxyAPI and hard-refresh Management Center. Discover quota support with `GET /v0/management/quota/providers`, then fetch the selected credential with `POST /v0/management/quota/fetch`. Both require the management key; the dashboard must support the generic v8 quota API.

**Original upstream changelog**: https://github.com/massiveits/opencode-go-cliproxyapi/compare/v0.1.9...v0.1.10
