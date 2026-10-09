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
- Restart CLIProxyAPI and hard-refresh Management Center. Quota management requests now use `/v0/management/plugins/cpa-opencode-go/quota-usage`, and the renamed session cache starts empty.

**Original upstream changelog**: https://github.com/massiveits/opencode-go-cliproxyapi/compare/v0.1.9...v0.1.10
