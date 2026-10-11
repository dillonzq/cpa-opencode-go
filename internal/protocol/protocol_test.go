package protocol

import (
	"bytes"
	"encoding/json"
	translator "github.com/dillonzq/api-translator"
	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
	"reflect"
	"strings"
	"testing"
)

func event(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }

var fixtures = []struct {
	route                          catalog.Route
	format, request, response, sse string
}{
	{catalog.RouteChatCompletions, "openai", `{"model":"client","messages":[{"role":"user","content":"hello"}]}`,
		`{"id":"r","model":"m","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		"data: {\"id\":\"r\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" + "data: {\"id\":\"r\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"},
	{catalog.RouteMessages, "claude", `{"model":"client","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
		`{"id":"r","model":"m","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`,
		event("message_start", `{"type":"message_start","message":{"id":"r","model":"m","usage":{"input_tokens":2}}}`) +
			event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
			event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`) +
			event("content_block_stop", `{"type":"content_block_stop","index":0}`) +
			event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`) +
			event("message_stop", `{"type":"message_stop"}`)},
	{catalog.RouteResponses, "openai-response", `{"model":"client","input":"hello"}`,
		`{"id":"r","model":"m","object":"response","status":"completed","output":[{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`,
		event("response.created", `{"type":"response.created","response":{"id":"r","model":"m"}}`) +
			event("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg","output_index":0,"content_index":0,"delta":"hello"}`) +
			event("response.completed", `{"type":"response.completed","response":{"id":"r","model":"m","object":"response","status":"completed","output":[{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`)},
}

func TestConversionMatrix(t *testing.T) {
	for _, source := range fixtures {
		for _, target := range fixtures {
			t.Run(source.format+"/"+target.format, func(t *testing.T) {
				state := translator.NewRequestState()
				request, err := ConvertRequest(target.route, "upstream", source.format, []byte(source.request), state)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]json.RawMessage
				if json.Unmarshal(request, &doc) != nil || string(doc["model"]) != `"upstream"` || !bytes.Contains(request, []byte("hello")) {
					t.Fatalf("request=%s", request)
				}
				// Response source is the upstream route; the client's requested output is independent.
				response, err := ConvertResponse(source.route, target.format, 200, []byte(source.response), state)
				if err != nil || !bytes.Contains(response, []byte("hello")) {
					t.Fatalf("response=%s err=%v", response, err)
				}
				var result map[string]json.RawMessage
				if json.Unmarshal(response, &result) != nil {
					t.Fatalf("invalid response=%s", response)
				}
				var usage map[string]int
				if json.Unmarshal(result["usage"], &usage) != nil {
					t.Fatal("missing usage")
				}
				input, output := "input_tokens", "output_tokens"
				if target.format == "openai" {
					input, output = "prompt_tokens", "completion_tokens"
				}
				if usage[input] != 2 || usage[output] != 1 {
					t.Fatalf("usage=%v", usage)
				}
				sc, err := NewStreamConverter(source.route, target.format, translator.NewRequestState())
				if err != nil {
					t.Fatal(err)
				}
				var events [][]byte
				for _, b := range []byte(source.sse) {
					out, _, err := sc.Feed([]byte{b})
					if err != nil {
						t.Fatal(err)
					}
					events = append(events, out...)
				}
				tail, err := sc.Finish()
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, tail...)
				joined := string(bytes.Join(events, nil))
				if !strings.Contains(joined, "hello") {
					t.Fatalf("stream=%s", joined)
				}
				if target.format == "openai" {
					for _, payload := range events {
						if !json.Valid(payload) {
							t.Fatalf("CPA requires bare JSON: %s", payload)
						}
					}
					if strings.Contains(joined, "[DONE]") || strings.Contains(joined, "data:") {
						t.Fatalf("duplicate host framing: %s", joined)
					}
				} else {
					var decoder translator.SSEDecoder
					seq := int64(-1)
					for _, frame := range events {
						decoded := decoder.Feed(frame)
						if len(decoded) != 1 || !json.Valid(decoded[0].Data) {
							t.Fatalf("expected complete frame=%s", frame)
						}
						if target.format == "openai-response" && source.format != target.format {
							var item struct {
								Sequence int64 `json:"sequence_number"`
							}
							json.Unmarshal(decoded[0].Data, &item)
							if item.Sequence != seq+1 {
								t.Fatalf("sequence=%d after %d", item.Sequence, seq)
							}
							seq = item.Sequence
						}
					}
				}
			})
		}
	}
}

func TestFormatAndErrorBridge(t *testing.T) {
	if _, err := Format("responses"); err == nil || err.Class != errclass.ClassUnsupported {
		t.Fatal(err)
	}
	if _, err := Route(catalog.Route("unknown")); err == nil || err.Class != errclass.ClassUnsupported {
		t.Fatal(err)
	}
	if _, err := NewStreamConverter(catalog.RouteMessages, "unknown", nil); err == nil {
		t.Fatal("invalid output accepted")
	}
	input := &translator.Error{Class: translator.ClassRateLimit, Message: "quota", StatusCode: 429, Retryable: true}
	want := &errclass.Error{Class: errclass.ClassRateLimit, Message: "quota", StatusCode: 429, Retryable: true}
	if !reflect.DeepEqual(Error(input), want) || Error(nil) != nil {
		t.Fatal("error metadata lost")
	}
	for _, message := range []string{"", " \t\n"} {
		input.Message = message
		got := Error(input)
		if strings.TrimSpace(got.Message) == "" || got.Class != want.Class || got.StatusCode != want.StatusCode || got.Retryable != want.Retryable {
			t.Fatalf("empty error lost metadata: %+v", got)
		}
	}
}

func TestChatMultilineSSEAndFailureEvents(t *testing.T) {
	sc, err := NewStreamConverter(catalog.RouteChatCompletions, "openai", nil)
	if err != nil {
		t.Fatal(err)
	}
	events, done, err := sc.Feed([]byte("data: {\r\ndata: \"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\r\n\r\ndata: {bad}\r\n\r\n"))
	if err == nil || done || len(events) != 1 || !json.Valid(events[0]) {
		t.Fatalf("events=%q done=%v err=%v", events, done, err)
	}
	if tail, err := sc.Finish(); err == nil || len(tail) != 0 {
		t.Fatalf("failure recovered: %q %v", tail, err)
	}
}

func BenchmarkMessagesStream(b *testing.B) {
	var wire strings.Builder
	wire.WriteString(event("message_start", `{"type":"message_start","message":{"id":"r","model":"m","usage":{"input_tokens":10}}}`))
	for i := 0; i < 256; i++ {
		wire.WriteString(event("content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"hej"}}`))
	}
	wire.WriteString(event("content_block_start", `{"index":1,"content_block":{"type":"tool_use","id":"toolu_a","name":"f"}}`))
	wire.WriteString(event("content_block_delta", `{"index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`))
	wire.WriteString(event("message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`))
	wire.WriteString(event("message_stop", `{"type":"message_stop"}`))
	payload := []byte(wire.String())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sc, err := NewStreamConverter(catalog.RouteMessages, "openai", nil)
		if err != nil {
			b.Fatal(err)
		}
		events, done, err := sc.Feed(payload)
		if err != nil || !done || len(events) == 0 {
			b.Fatalf("%v %v", done, err)
		}
	}
}

func TestReasoningAcrossProtocolBoundaries(t *testing.T) {
	const thought = " 思考🙂 "
	for _, source := range fixtures {
		body, wire := source.response, source.sse
		switch source.format {
		case "openai":
			body = strings.Replace(body, `"content":"hello"`, `"content":"hello","reasoning_content":"`+thought+`"`, 1)
			wire = `data: {"id":"r","choices":[{"delta":{"reasoning_content":"` + thought + `"}}]}` + "\n\n" + wire
		case "claude":
			body = strings.Replace(body, `"content":[`, `"content":[{"type":"thinking","thinking":"`+thought+`"},`, 1)
			thinking := event("content_block_start", `{"index":1,"content_block":{"type":"thinking","thinking":""}}`) + event("content_block_delta", `{"index":1,"delta":{"type":"thinking_delta","thinking":"`+thought+`"}}`) + event("content_block_stop", `{"index":1}`)
			wire = strings.Replace(wire, "event: content_block_start", thinking+"event: content_block_start", 1)
		case "openai-response":
			body = strings.Replace(body, `"output":[`, `"output":[{"id":"rs","type":"reasoning","summary":[{"type":"summary_text","text":"`+thought+`"}]},`, 1)
			wire = strings.Replace(wire, "event: response.output_text.delta", event("response.reasoning_summary_text.delta", `{"item_id":"rs","summary_index":0,"delta":"`+thought+`"}`)+"event: response.output_text.delta", 1)
		}
		for _, target := range fixtures {
			if source.format == target.format {
				continue
			}
			t.Run(source.format+"/"+target.format, func(t *testing.T) {
				out, err := ConvertResponse(source.route, target.format, 200, []byte(body), nil)
				if err != nil || !bytes.Contains(out, []byte(thought)) {
					t.Fatalf("reasoning lost: %s %v", out, err)
				}
				var doc map[string]json.RawMessage
				json.Unmarshal(out, &doc)
				switch target.format {
				case "openai":
					// Decode fields individually so reasoning cannot silently merge into content.
					var raw []struct{ Message map[string]string }
					json.Unmarshal(doc["choices"], &raw)
					if raw[0].Message["content"] != "hello" || raw[0].Message["reasoning_content"] != thought {
						t.Fatalf("mixed reasoning: %s", out)
					}
				case "claude":
					var blocks []struct{ Type, Text, Thinking string }
					json.Unmarshal(doc["content"], &blocks)
					if len(blocks) < 2 || blocks[0].Type != "thinking" || blocks[0].Thinking != thought {
						t.Fatalf("missing thinking block: %s", out)
					}
				case "openai-response":
					var items []struct {
						Type    string
						Summary []struct{ Text string }
					}
					json.Unmarshal(doc["output"], &items)
					found := false
					for _, item := range items {
						if item.Type == "reasoning" && len(item.Summary) > 0 && item.Summary[0].Text == thought {
							found = true
						}
					}
					if !found {
						t.Fatalf("missing summary: %s", out)
					}
				}
				sc, err := NewStreamConverter(source.route, target.format, nil)
				if err != nil {
					t.Fatal(err)
				}
				var events [][]byte
				for _, b := range []byte(wire) {
					part, _, err := sc.Feed([]byte{b})
					if err != nil {
						t.Fatal(err)
					}
					events = append(events, part...)
				}
				tail, err := sc.Finish()
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, tail...)
				if !bytes.Contains(bytes.Join(events, nil), []byte(thought)) {
					t.Fatal("stream reasoning lost")
				}
			})
		}
	}
}

func TestEmptyStreamErrorRemainsFailure(t *testing.T) {
	for _, output := range []string{"openai", "claude", "openai-response"} {
		t.Run(output, func(t *testing.T) {
			sc, err := NewStreamConverter(catalog.RouteResponses, output, nil)
			if err != nil {
				t.Fatal(err)
			}
			wire := event("response.created", `{"type":"response.created","response":{"id":"r","model":"m"}}`) +
				event("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg","delta":"hello"}`) +
				event("error", `{"type":"error","message":"","status_code":500}`)
			_, done, err := sc.Feed([]byte(wire))
			if err == nil || done || err.Message == "" || err.Class != errclass.ClassUpstream || err.StatusCode != 500 || !err.Retryable {
				t.Fatalf("empty error became clean host close: done=%v err=%+v", done, err)
			}
			if _, err := sc.Finish(); err == nil || err.Message == "" {
				t.Fatalf("failure lost on Finish: %+v", err)
			}
		})
	}
}
