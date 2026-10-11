package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
)

// Payload is the host's effective input, already in SourceFormat. Format is
// the requested output format (prepareExecutorCall in CPA v8.0.0).
func (r executorRequest) effectivePayload() []byte {
	if r.Payload != nil {
		return r.Payload
	}
	return r.OriginalRequest // legacy callers omitted/null Payload
}
func (r executorRequest) inputFormat() string {
	if r.SourceFormat != "" {
		return r.SourceFormat
	}
	if r.Format != "" {
		return r.Format
	}
	return "openai"
}
func (r executorRequest) outputFormat() string {
	if r.Format != "" {
		return r.Format
	}
	return r.inputFormat()
}

const emptyOpenCodeSessionID = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func resolveOpenCodeSessionID(req executorRequest) (string, *errclass.Error) {
	if sid, ok := req.Metadata["canonical_session_id"].(string); ok && sid != "" {
		return sid, nil
	}
	for _, name := range []string{"X-Session-Affinity", "X-Opencode-Session", "X-Session-Id", "X-Claude-Code-Session-Id", "Session-Id"} {
		if sid := req.Headers.Get(name); sid != "" {
			return sid, nil
		}
	}
	return deriveOpenCodeSessionID(req.inputFormat(), req.effectivePayload())
}

// Session extraction deliberately does not use translation decoders: native
// protocols can carry files and future content types that cannot be translated.
// Known text/image/tool-result content keeps its historical concatenation hash;
// other content contributes canonical JSON with a delimiter. Never log it.
func deriveOpenCodeSessionID(format string, body []byte) (string, *errclass.Error) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return "", errclass.Translation("malformed session request JSON")
	}
	var content strings.Builder
	var items []map[string]json.RawMessage
	field := "messages"
	if format == "openai-response" {
		field = "input"
		var plain string
		if json.Unmarshal(doc[field], &plain) == nil {
			content.WriteString(plain)
			return sessionDigest(content.String()), nil
		}
	} else if format != "openai" && format != "claude" {
		return "", &errclass.Error{Class: errclass.ClassUnsupported, Message: "unsupported protocol format for OpenCode Go session derivation: " + format}
	}
	// Non-message input shapes are left to the adapter/upstream to validate.
	_ = json.Unmarshal(doc[field], &items)
	started := false
	for _, item := range items {
		role, typ := sessionString(item["role"]), sessionString(item["type"])
		user := role == "user" && (format != "openai-response" || typ == "message" || typ == "")
		if !started {
			if !user {
				if format == "openai" && role != "system" && role != "developer" {
					break
				}
				continue
			}
			started = true
		} else if !user {
			break
		}
		appendSessionContent(&content, item["content"], format)
	}
	return sessionDigest(content.String()), nil
}

func sessionDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
func sessionString(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }

func appendSessionContent(out *strings.Builder, raw json.RawMessage, format string) {
	if !hasContent(raw) {
		return
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		out.WriteString(s)
		return
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		appendSessionJSON(out, raw)
		return
	}
	for _, part := range parts {
		var p map[string]json.RawMessage
		if json.Unmarshal(part, &p) != nil {
			appendSessionJSON(out, part)
			continue
		}
		switch sessionString(p["type"]) {
		case "text", "input_text", "output_text":
			out.WriteString(sessionString(p["text"]))
		case "image_url", "input_image":
			imageURL := sessionString(p["image_url"])
			if imageURL == "" {
				var image map[string]json.RawMessage
				_ = json.Unmarshal(p["image_url"], &image)
				imageURL = sessionString(image["url"])
			}
			if imageURL != "" {
				out.WriteString(imageURL) // preserve existing URL-based hashes
			} else {
				appendSessionJSON(out, part) // native file_id and future image variants
			}
		case "image":
			if format != "claude" {
				appendSessionJSON(out, part)
				continue
			}
			var src map[string]json.RawMessage
			_ = json.Unmarshal(p["source"], &src)
			switch sessionString(src["type"]) {
			case "url":
				out.WriteString(sessionString(src["url"]))
			case "base64":
				media := sessionString(src["media_type"])
				if media == "" {
					media = "application/octet-stream"
				}
				out.WriteString("data:" + media + ";base64," + sessionString(src["data"]))
			default:
				appendSessionJSON(out, part)
			}
		case "tool_result":
			if format == "claude" {
				appendSessionContent(out, p["content"], format)
			} else {
				appendSessionJSON(out, part)
			}
		case "thinking", "redacted_thinking", "tool_use":
			if format != "claude" {
				appendSessionJSON(out, part)
			} // historically omitted from Claude hashes
		default:
			appendSessionJSON(out, part)
		}
	}
}

func appendSessionJSON(out *strings.Builder, raw json.RawMessage) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return
	} // enclosing JSON was already validated
	if object, ok := value.(map[string]any); ok {
		delete(object, "cache_control")
	}
	canonical, err := json.Marshal(value) // stable map key order, exact numbers
	if err != nil {
		return
	}
	out.WriteByte(0)
	out.Write(canonical)
	out.WriteByte(0)
}
