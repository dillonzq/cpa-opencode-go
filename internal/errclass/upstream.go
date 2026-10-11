package errclass

import (
	"encoding/json"
	"strings"
)

// RedactedSnippet bearer-redacts and truncates a payload snippet for error
// messages — never an upstream body echo (FR-011).
func RedactedSnippet(s string) string {
	s = Redact(s)
	if len(s) > 256 {
		return s[:256] + "..."
	}
	return s
}

// snippetBound bounds redaction work: only the head of an oversized error
// body can reach the 256-char snippet, so every upstream >=400 site
// truncates to this size before scanning (FR-011).
const snippetBound = 4096

// extractErrorMessage attempts to extract a clean, human-readable error
// message from upstream JSON error bodies (e.g. OpenAI or Anthropic format).
func extractErrorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	switch v := probe.Error.(type) {
	case string:
		if v != "" {
			return strings.TrimSpace(v)
		}
	case map[string]any:
		if m, ok := v["message"].(string); ok && m != "" {
			return strings.TrimSpace(m)
		}
	}
	if probe.Message != "" {
		return strings.TrimSpace(probe.Message)
	}
	if probe.Detail != "" {
		return strings.TrimSpace(probe.Detail)
	}
	return ""
}

// UpstreamStatusError classifies an upstream >=400 response body into one
// kernel used by every adapter and the executor: the body is truncated to
// its head, decoded for human-readable error messages if JSON, or fallen
// back to a redacted snippet, then classified per §7 via FromStatus.
// One kernel keeps bounding and redaction from diverging across call sites.
func UpstreamStatusError(status int, body []byte) *Error {
	if len(body) > snippetBound {
		body = body[:snippetBound]
	}
	msg := extractErrorMessage(body)
	if msg == "" {
		msg = string(body)
	}
	return FromStatus(status, RedactedSnippet(msg))
}
