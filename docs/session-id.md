# OpenCode Go session ID compatibility

[中文](session-id.zh-CN.md) · [Back to README](../README.md)

This document describes how the current plugin selects conversation identity and sends it to OpenCode Go. Reference: [OpenCode v2 Go documentation](https://dev.opencode.ai/v2/docs/console/go#where-can-i-use-it), checked on 2026-10-11.

## Official expectations

OpenCode Go asks clients to send a stable session ID in `x-opencode-session` for each conversation to optimize routing and prompt caching. The documentation also says Go recognizes native session headers from Claude Code, Codex, and ZCode; proxies should preserve session information. That page does not require UUIDs or a `ses_` prefix.

This document concerns the session header sent to Go inference endpoints. The plugin does not call an OpenCode instance's session creation, retrieval, or persistence APIs.

## Selection order

The plugin takes the first nonempty value in this order and writes it to the upstream `x-opencode-session` header:

| Priority | Source |
| --- | --- |
| 1 | Host `Metadata["canonical_session_id"]`, which must be a nonempty string |
| 2 | `X-Session-Affinity` |
| 3 | `X-Opencode-Session` |
| 4 | `X-Session-Id` |
| 5 | `X-Claude-Code-Session-Id` |
| 6 | `Session-Id` |
| 7 | SHA-256 of the initial user content in the effective input |

The host canonical ID takes precedence over every client header, so the final value may differ from the original client ID. The plugin itself does not trim explicit IDs, add prefixes, convert them to UUIDs, or hash them again; only empty strings are skipped. Headers are read using Go's `http.Header.Get`.

The plugin does not directly extract identity fields such as body `session_id`, `thread_id`, or Claude `metadata.user_id`. If the host recognizes these fields, it can supply the result through `canonical_session_id`.

## Effective input and legacy callers

Hashing uses the host's effective `Payload` before plugin native normalization and protocol conversion, including interceptor changes. It falls back to `OriginalRequest` only when `Payload` is nil (omitted or null); an explicit empty payload does not replay the original request.

Input format is selected from `SourceFormat`, then `Format`, then `openai` if both are absent. In normal calls, `SourceFormat` identifies the input protocol and `Format` selects the output protocol.

## Content hashing

The final value is the SHA-256 of the extracted content, encoded as 64 lowercase hexadecimal characters. The plugin does not generate random IDs or maintain a session table.

| Input format | Extraction scope |
| --- | --- |
| `openai` (Chat Completions) | Skip leading system/developer messages, then collect the first consecutive user messages; stop at any other role, including assistant/tool before the first user |
| `claude` (Messages) | Skip non-user messages before the first user, collect the first consecutive user messages, then stop at a non-user message |
| `openai-response` (Responses) | Hash string `input` directly; for arrays, skip items before the first user message, collect consecutive user messages (`role: user` with type message or absent), then stop at any other item |

System instructions, message-level ID/status, later conversation turns, model, and authentication information do not contribute to the plugin fallback hash. Identical plain text produces the same ID across all three formats; text whitespace and line endings are preserved.

| Content | Compatibility behavior |
| --- | --- |
| Strings and text/input_text/output_text | Concatenate text in order, preserving historical hashes |
| image_url/input_image with a URL | Concatenate the URL, preserving historical hashes |
| Claude image | Concatenate URLs; convert base64 sources to data URLs, defaulting missing MIME to application/octet-stream |
| Claude tool_result | Recursively extract content, preserving historical hashes |
| Claude thinking/redacted_thinking/tool_use | Omit from the hash, preserving historical behavior |
| Files, images with only file_id, and unknown content | Include stable JSON encoding with NUL delimiters on both sides; object keys have stable ordering, json.Number preserves numeric precision, and the content object's top-level cache_control is removed |

Session extraction does not call protocol translation decoders or validate unrelated native fields, so files and future content types are not rejected solely by session extraction. Unsupported formats or top-level JSON decoding failures return errors; other input shapes are left to the request adapter or upstream to validate. An explicit ID bypasses content hashing, but the request still undergoes subsequent processing.

When no content is extracted, the plugin uses the fixed SHA-256 of an empty string:

```text
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

## Request coverage and limitations

Streaming and non-streaming inference requests for Chat Completions, Messages, and Responses all set `x-opencode-session`. Model catalog `/models` requests do not include it. Extraction does not log the content used for hashing; debug logs include the final session ID.

Content hashing is a deterministic fallback that cannot fully distinguish real conversations. Identical initial content produces the same ID; history trimming, context compaction, or interceptor changes to initial content may change it; requests without extractable content share the empty-content hash. Historical text concatenation also allows different splits to produce the same result, such as `ab` + `c` and `a` + `bc`.

To preserve stable identity for each conversation, prefer an explicit client session ID or a host canonical ID. The fallback provides an identifier to send; it does not establish that every ID has been verified against the real upstream.

## Implementation and verification

- [session.go](../internal/plugin/session.go): selection order, effective input, and hashing.
- [executor.go](../internal/plugin/executor.go): streaming/non-streaming execution and upstream headers.
- [session_test.go](../internal/plugin/session_test.go): precedence, historical hashes, cross-format parity, native files, and unknown content.
- [executor_contract_test.go](../internal/plugin/executor_contract_test.go): explicit IDs, native files, and empty payload behavior.
- [integration_test.go](../internal/plugin/integration_test.go): headers for three protocols and catalog requests against a mock upstream.

Targeted verification: `go test ./internal/plugin -run 'Session|DeriveOpenCode' -count=1`. These offline tests do not replace real upstream validation.
