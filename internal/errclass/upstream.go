package errclass

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
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

// ErrorBodyLimit bounds redaction work: only the head of an oversized error
// body can reach the 256-char snippet, so every upstream >=400 site
// truncates to this size before scanning (FR-011).
const ErrorBodyLimit = 4096

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
	if len(body) > ErrorBodyLimit {
		body = body[:ErrorBodyLimit]
	}
	msg := extractErrorMessage(body)
	if msg == "" {
		msg = string(body)
	}
	return FromStatus(status, RedactedSnippet(msg))
}

// goUsageLimitType is the error type OpenCode Go returns when one of the
// key's 5 hour, weekly, or monthly usage windows is exhausted.
const goUsageLimitType = "GoUsageLimitError"

// UpstreamResponseError classifies an upstream >=400 response like
// UpstreamStatusError and also reads the response headers. A 429 Go usage
// limit refusal applies to every model on the key until the window
// resets, so it is marked credential-scoped and carries the upstream
// retry-after hint; the host then cools the whole key instead of one
// model.
func UpstreamResponseError(status int, headers http.Header, body []byte) *Error {
	e := UpstreamStatusError(status, body)
	if status != http.StatusTooManyRequests {
		return e
	}
	if len(body) > ErrorBodyLimit {
		body = body[:ErrorBodyLimit]
	}
	var probe struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
		Metadata struct {
			LimitName string `json:"limitName"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &probe) != nil || probe.Error.Type != goUsageLimitType {
		return e
	}
	e.Class = ClassQuota
	e.CredentialScoped = true
	e.RetryAfterSeconds = retryAfterSeconds(headers.Get("Retry-After"), time.Now())
	if name := strings.TrimSpace(probe.Metadata.LimitName); name != "" {
		e.Message = RedactedSnippet(e.Message + " (" + name + " window)")
	}
	return e
}

// retryAfterSeconds parses a Retry-After value given as delay seconds or
// an HTTP date, rounding up so the key is never retried before the reset.
// Missing, invalid, or past values yield 0 (no hint).
func retryAfterSeconds(value string, now time.Time) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		return max(secs, 0)
	}
	at, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	d := at.Sub(now)
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}
