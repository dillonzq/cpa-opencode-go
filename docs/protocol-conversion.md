# Protocol conversion: preservation and losses

[简体中文](protocol-conversion.zh-CN.md) · [Back to README](../README.md)

Requests, regular responses, and streaming responses use pinned [api-translator v0.2.0](https://github.com/dillonzq/api-translator/tree/v0.2.0). All six cross-protocol directions and same-protocol processing are supported for Chat Completions, Messages, and Responses. See the library's [protocol guide](https://github.com/dillonzq/api-translator/blob/v0.2.0/docs/protocol-conversion.md) for field mappings and limits. This document describes the plugin's host and upstream policies.

## Integration

CPA formats `openai`, `claude`, and `openai-response` map to `chat-completions`, `messages`, and `responses`. The plugin executes effective `Payload` in `SourceFormat`; only omitted/null Payload falls back to OriginalRequest. Explicit empty payload never replays the original. `Format` independently selects the output protocol. Unknown formats fail before contacting upstream.

Each execution owns a separate RequestState shared by request and response conversion. The default is LossPolicyAllow, which collects known library loss diagnostics without rejecting solely for those losses. No new user configuration is introduced. Protocol errors and unrepresentable actual output still fail.

The plugin retains HTTP, authentication, routing, sessions, cancellation, timeouts, response byte limits, and CPA error envelopes. Messages uses x-api-key and anthropic-version: 2023-06-01; other protocols use Bearer. All include x-opencode-session. Sessions are derived before cleaning or conversion, with canonical ID, explicit headers, and initial user content precedence preserved.

## Native upstream compatibility

The library's same-protocol request conversion only rewrites model. Before that, the plugin retains OpenCode Go policies using RawMessage to preserve unknown fields and exact numbers:

| Native path | Plugin policy |
| --- | --- |
| Chat → Chat | Rewrite developer to system; remove top-level thinking without a valid string type |
| Messages → Messages | Fold historical system turns into top-level system; omit their thinking/redacted_thinking; reject images and unknown system blocks; without system turns, only rewrite model |
| Responses → GPT Responses | Only rewrite model; preserve native tools, reasoning, compaction, and other fields |
| Responses → non-GPT Responses | Drop historical reasoning/compaction; filter custom apply_patch, tool_search, image_generation; reduce web_search declarations to type; then call NormalizeResponsesRequest to merge additional_tools, flatten namespace/custom tools, and register identities for response restoration |

The GPT branch retains the case-insensitive `gpt` upstream ID prefix check. Custom input uses a string parameter wrapper; non-string historical custom input fails. Duplicate declarations of the same identity keep the first; flattened identity collisions fail. Upstream acceptance of native hosted tools remains model-dependent.

## Cross-protocol behavior

| Content | Behavior and limits |
| --- | --- |
| Text, input images, and tools | Preserve representable content and function calls; reject unknown/unrepresentable input. Cross-protocol custom apply_patch can convert like other custom tools. Responses output restores namespace/custom identities |
| Visible reasoning | Map to reasoning_content, thinking, or reasoning summaries, preserving Unicode and whitespace separately from the answer |
| Signatures and opaque data | Omit Anthropic signatures/redacted_thinking.data and Responses encrypted_content across protocols. Unsigned thinking is not guaranteed replayable to models requiring valid signatures |
| Refusal and usage | Preserve representable refusals, basic tokens, cache read/write, and reasoning tokens; distinguish missing details from explicit zero; recompute Chat/Responses totals |
| Structured output | Convert JSON Schema between response_format.json_schema, text.format, and output_config.format; preserve tool strict. Messages rejects json_object without a schema |
| Hosted search and citations | Responses ↔ Messages converts representable search and URL citations; Chat cannot represent hosted search execution. Target model support is checked by upstream; encrypted search data and unrepresentable offsets may be lost |
| Tool output images | Responses → Messages retains text/image order inside tool_result. Responses → Chat keeps tool replies contiguous, then adds user content labeled by call ID; requires image_url, not file-ID-only references |
| Unknown output | Inspect raw payload including unmodeled result/action; reject unrepresentable actual output. Audio and image-generation output remain incomplete |
| Candidates and boundaries | Cross-protocol conversion handles only the first choice; Chat aggregates text; original message/block ordering and extension fields are not guaranteed preserved |
| Status | Responses failed/cancelled/protocol errors return classified failures; non-terminal regular cross-protocol responses fail; incomplete maps to truncation |

Historical reasoning, compaction, and signatures remain upstream-dependent. CPA usage reasoning labels still come from the effective input payload, so suffix-only controls and suffix overrides require host changes to correct labels; token totals are unaffected.

## Thinking suffixes

The plugin strips trailing model(value) for catalog lookup, then tries the literal ID if lookup fails. Literal matches do not apply a suffix. CPA may reject a literal parenthesized ID before it reaches the plugin.

Recognized suffixes remove superseded request controls before conversion and apply their control afterward, taking priority over the body. none/0 disables, auto/-1 uses upstream defaults, and levels range from minimal to max. Messages retains exact numeric suffix budgets; Chat/Responses use fixed effort thresholds. Unknown suffixes are stripped without changing request controls. Budgets are not clamped to model capabilities or max_tokens.

## SSE and failures

The library handles arbitrary network chunks, multiple data lines, LF/CRLF/CR, interleaved tools, late identities, and deduplicated done/item/terminal snapshots. Synthesized Responses events have increasing sequence_number values.

Library output is complete SSE frames. For Chat output, the plugin uses SSEDecoder to extract bare JSON and consume library DONE; CPA's HTTP handler wraps data and sends one DONE on normal close. Messages/Responses keep complete frames. SSE comments/retry/id/ping are not guaranteed preserved across protocols.

When Feed/Finish returns both events and an error, completed events are emitted before closing with the error, without successful completion. On EOF before done, Finish checks terminal validity. Cancellation, timeout, and network errors close directly as failures. Chat finish_reason/DONE and Messages stop_reason/message_stop can establish termination; Responses requires a valid terminal event. Failed conversion cannot recover or resend upstream requests. Empty or whitespace-only library error messages receive a fallback message so CPA does not interpret error closure as success.

Implementation: internal/protocol wraps public library APIs; internal/plugin/upstream_policy.go holds native cleaning; reasoning_suffix.go uses library thinking; executor.go owns transport. Tests cover 27 baseline scenarios, native policies, request state isolation, and real CPA HTTP streaming. Offline tests do not replace real upstream validation.
