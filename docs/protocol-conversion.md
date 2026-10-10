# Protocol conversion: preservation and loss

[简体中文](protocol-conversion.zh-CN.md) · [Back to README](../README.md)

This guide records the plugin's current behavior. Response reasoning mappings follow the pinned **CPA v8.0.0** dependency; this does not imply parity with every official translator feature. Response conversion remains implemented by the plugin; the official CPA translators are not imported.

## Response reasoning

Both regular and streaming responses support all six conversion directions:

| Upstream → client | Preservation |
| --- | --- |
| Chat Completions → Messages | Map visible reasoning to `thinking` blocks / `thinking_delta` |
| Chat Completions → Responses | Map visible reasoning to `reasoning.summary[]` (`type: summary_text`) and corresponding summary stream events |
| Messages → Chat Completions | Concatenate `thinking` text into `message.reasoning_content` / `delta.reasoning_content` |
| Messages → Responses | Map each `thinking` block to a separate reasoning summary item |
| Responses → Chat Completions | Concatenate reasoning items' `summary_text` and readable `content` (`reasoning_text` / `text`) into `reasoning_content` |
| Responses → Messages | Map the same visible reasoning to `thinking` blocks / `thinking_delta` |

Chat Completions selects the first field containing readable text in this order: `reasoning_content` → `reasoning` → `reasoning_details`. Supported shapes include strings, arrays, and objects with a `text` field. Aliases are not concatenated together. Whitespace and Unicode are preserved; reasoning is kept separate from the final answer, and absent reasoning is never invented. Multiple Chat Completions reasoning fragments aggregate into one reasoning item in the terminal Responses result. Multiple Messages thinking blocks remain separate reasoning items. Chat Completions output cannot retain the original interleaving of answer text, reasoning, and individual blocks.

Streaming Responses conversion accepts `response.reasoning_summary_text.delta/done` and `response.reasoning_text.delta/done`. Reasoning snapshots in `output_item` events and terminal responses fill in text not delivered through deltas. Previously emitted prefixes of the same part are not replayed. Synthesized Responses streams include item/summary start, delta, and completion events. Terminal output order and indexes match item announcements.

Cross-protocol conversion still omits Anthropic `signature` / `signature_delta` / `redacted_thinking.data` and Responses `encrypted_content`. These provider-specific signatures and opaque data have no reliable cross-protocol mapping. They are neither fabricated nor treated as readable reasoning. Converted thinking without signatures is suitable for display; it is not guaranteed to be replayable in Anthropic requests that require valid signatures.

Same-protocol responses generally pass through unchanged, preserving these fields. Responses custom-tool restoration and streaming error/terminal handling are exceptions.

Official source references: [Chat Completions → Messages](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.0/internal/translator/openai/claude/openai_claude_response.go), [Messages → Chat Completions](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.0/internal/translator/claude/openai/chat-completions/claude_openai_response.go), [Chat Completions → Responses](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.0/internal/translator/openai/openai/responses/openai_openai-responses_response.go), and [Responses → Chat Completions](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.0/internal/translator/codex/openai/chat-completions/codex_openai_response.go).

## Other response fields

The following table describes supported mappings and known losses.

| Content | Default behavior / loss |
| --- | --- |
| Text and function tool calls | Preserve text and arguments through conversion. Missing arguments default to `{}`. Messages conversion parses arguments and reports invalid JSON |
| Multiple `choices` | Cross-protocol conversion handles only the first choice. Same-protocol passthrough preserves all choices |
| Multiple messages / text blocks | Chat Completions aggregates answer text. Synthesized Responses aggregates text into one assistant message at the first text position |
| Chat Completions `message.refusal` and `refusal` parts in Responses messages | Preserved in regular and streaming responses. Chat Completions ↔ Responses retains a separate refusal field/part; Messages receives displayable text. Normally completed refusals map to content_filter/refusal, while tool and truncation signals retain precedence |
| Citations, `annotations`, logprobs, audio, and provider extensions | Not copied across protocols; corresponding delta events are ignored by default |
| Images and other output types | Chat Completions → Messages converts supported image blocks. Chat Completions → Responses rejects non-text output. Messages rejects unsupported output blocks. Responses message parts other than `output_text` and `refusal` are ignored. Unknown output items produce errors only when decoded `content/name/call_id/arguments` fields contain payload; items with only unmodeled fields such as `result/action` can be silently lost |
| Responses tool-specific events | Convert `function_call` and argument delta/done events. Output item and terminal snapshots fill missing argument suffixes; call_id/item_id share state to prevent duplicate tool calls. Real call_id and name are tracked separately from argument state. Argument delta/done events are buffered while identity is incomplete; a complete tool item triggers exactly one announcement. A terminal response with unresolved identity reports an error. Messages conversion queues subsequent text, reasoning, and tools behind an unfinished tool while continuing to stream its arguments. The block closes only after completion or terminal reconciliation and is never reopened for the same call. Search, image generation, and other specialized events remain ignored by default |
| Responses text/refusal snapshots | output_text/refusal done, output item, and terminal snapshots fill missing suffixes. Deduplicate by item_id, content_index, and part type. Messages has no separate refusal field, so text follows arrival order. Synthesized Responses text/refusal part indexes match the terminal result |
| Finish reasons | `stop` ↔ `end_turn`, `length` ↔ `max_tokens`, `tool_calls` ↔ `tool_use`, and `content_filter` ↔ `refusal`. Unknown reasons fall back to ordinary completion. The specific `stop_sequence` value is not copied |
| Tool calls alongside truncation | Actual tool calls take precedence over truncation when converting to Chat Completions / Messages. Responses still maps `length/max_tokens` to `incomplete`, with tool calls represented in output items |
| Responses status | `incomplete` → `length/max_tokens`; other non-stream statuses fall back to ordinary completion. Synthesized Responses expresses only completed/incomplete and does not copy `incomplete_details` or the full response configuration |
| Basic usage | Map input/output token counts. Chat Completions/Responses totals are recomputed as input + output. Missing usage is synthesized as zero |
| Cache usage | Messages input excludes cached tokens, so conversion to Chat Completions/Responses adds cache read + creation. Conversion back to Messages subtracts representable cache counts and clamps the result to zero or above. Cache read is preserved. Cache creation maps across all three protocols as cache_creation_input_tokens / cache_write_tokens. Missing details remain absent, explicit zeros survive, and input totals are not counted twice |
| Reasoning token usage | Chat Completions ↔ Responses preserves `reasoning_tokens`. Messages has no separate detail; those tokens remain in total output usage |
| Identity and timestamps | Preserve response id/model. Synthesized message/reasoning item IDs derive from the response ID. Regular cross-protocol Chat Completions uses the current time for created; Responses streams converted to Chat Completions can use upstream created_at. Other unmodeled identity/time fields are not copied |
| SSE framing | Rebuild events in the target protocol. Comments, retry, id, ping, and similar metadata are not mapped. Chat Completions still parses each data line separately and does not support one JSON document split across multiple data lines |

## Request omissions and mappings

| Content / route | Current behavior |
| --- | --- |
| Historical reasoning | Messages → Chat Completions/Responses drops thinking/redacted_thinking. Responses → Chat Completions drops reasoning items. Chat Completions → Messages/Responses does not map historical reasoning fields. Responses → Messages preserves summary text as unsigned thinking |
| Native Responses requests for non-GPT models | Remove historical reasoning and compaction. The native GPT path only rewrites model, preserving other content |
| Reasoning controls | `reasoning_effort` ↔ `reasoning.effort`. Messages uses a fixed budget table; the reverse mapping uses budget thresholds, so exact round trips are not possible. `none` disables reasoning; `auto` defers to upstream defaults. Budgets are not filtered or clamped by model capabilities |
| Model-name thinking suffixes | CPA parses a trailing `model(value)` suffix only on its own executor paths, so the plugin strips it for catalog routing and applies the control itself, with suffix priority: a recognized suffix replaces whatever reasoning control the body carried (the superseded control is dropped before conversion so an unrepresentable value cannot fail a request the suffix already replaced). `none`/`0` disables, `auto`/`-1` removes the control so the upstream applies its own default, levels are `minimal`–`max`, and a numeric value maps to a level with the fixed thresholds. On Messages a level becomes an enabled budget from the fixed table, a numeric value keeps its exact budget, `none` also drops `thinking.display` (it only applies to an active block), `output_config.effort` is always superseded, and `auto` removes the whole control. Chat Completions writes `reasoning_effort`, Responses writes `reasoning.effort` and keeps sibling reasoning fields. An unrecognized value strips the suffix only and leaves the body's own control alone. Budgets are not clamped against `max_tokens` or model limits, matching the reasoning-control policy above; a literal catalog ID containing parentheses is reachable only when the stripped name does not resolve, and only at the plugin's own lookup layer — CPA resolves the requested model against its auth registry first, so such an ID may be rejected before the plugin is reached |
| system/developer/instructions | Convert to the target's system/instructions format. Messages also merges user/tool_result content and consecutive assistant content. Original message boundaries are not guaranteed to survive |
| Token limits | Map `max_tokens/max_completion_tokens/max_output_tokens`. Messages requires a positive limit and defaults missing or nonpositive values to 4096. Responses → Chat Completions omits nonpositive max_output_tokens |
| stop/stop_sequences | Preserve between Chat Completions and Messages; drop when converting to Responses |
| temperature/top_p | Map across protocols. Other protocol-specific fields are dropped through struct selection, including `n`, `seed`, penalties, logprobs, `logit_bias`, `response_format`, `metadata`, `store`, `service_tier`, `stream_options`, `previous_response_id`, `include`, and tool strict/cache_control fields. Native paths generally preserve them |
| Empty content and content types | Empty text blocks/messages without usable content are generally removed. Unknown cross-protocol input types, system images, and unrepresentable tool results produce explicit errors rather than being uniformly dropped |
| Responses tool output images | `function_call_output` and `custom_tool_call_output` accept supported text/image parts. Conversion to Messages preserves them inside `tool_result`. Conversion to Chat Completions emits text-only tool replies together, then mixed results as a user message labeled with call IDs, preserving text/image order; empty messages do not split parallel tool replies. Image parts require an `image_url`; file-ID-only references are unsupported |
| Tool choice / parallel calls | Convert auto/none/required/named-tool selections. Messages maps parallel control to disable_parallel_tool_use. Unrepresentable combinations produce errors |
| Responses tool declarations | Merge `additional_tools` into tools. Flatten namespaces, using truncation plus a digest for long names, and restore original identities in responses. Convert ordinary custom tools to functions with JSON-wrapped input; restore custom calls in Responses output |
| Incompatible Responses tools | Paths requiring tool normalization remove custom `apply_patch`, `tool_search`, and `image_generation`. Conversion to Chat Completions/Messages also removes hosted `web_search/web_search_preview`. Native GPT Responses passthrough skips this normalization. Chat Completions → Messages/Responses skips namespace declarations |
| Native Responses search tools for non-GPT models | Rebuild `web_search` declarations with only type, dropping extra options. Unknown top-level fields remain intact |
| Native Chat Completions requests | Change developer roles to system. Remove top-level thinking objects without a valid string type |
| Native Messages requests | Fold historical system turns into the top-level system field, omitting their thinking/redacted_thinking content. Without system turns, only model is rewritten |

Implementation entry points: `internal/adapter/*/request.go`, `convert.go`, and `stream.go`. Shared tool normalization lives in `internal/adapter/shared/responses_tools.go`; reasoning control tables and model-name suffix parsing live in `internal/thinking`, and suffix application lives in `internal/plugin/reasoning_suffix.go`.

## Further compatibility work

CPA labels a usage record's reasoning effort from the effective payload the host
passed to the plugin, not from the suffix or from the body the plugin sent upstream,
so a suffix-only request logs no effort and a suffix that overrides a body control
logs the overridden value. Token accounting is unaffected. Changing this needs a
host-side fix; the plugin has no callback for it.

Reasoning, refusal text, cache-write usage, and Responses text/tool-argument completion snapshots are now supported. The following gaps remain; support is not yet promised:

| Priority | Current gap / next direction |
| --- | --- |
| Complete native Responses custom-tool lifecycle | Native streams currently restore items and input done events, but function argument deltas and terminal output still need consistent restoration. Cross-protocol custom_tool_call/input deltas also need dedicated support |
| Structured output and tool strict | Check representable mappings between Chat Completions response_format and Responses text.format, preserve tool strict, and explicitly check compatibility when converting to Messages |
| Multimodal and unknown output | Support representable image-generation results, audio, and related content. Report unmodeled result/action payloads explicitly rather than silently completing |
| Citations and annotations | Preserve citations, sources, and offsets supported by the target protocol; establish explicit handling for fields that cannot be mapped without loss |
| SSE and tool metadata | Move Chat Completions to complete SSE frame parsing for multiple data lines. Responses → Messages now serializes interleaved tool blocks; other paths still need late/fragmented tool id/name metadata and interleaved lifecycle handling |

Historical reasoning replay, compaction records, and provider signatures should continue to follow upstream capabilities. Preserving fields alone does not justify sending incompatible payloads.
