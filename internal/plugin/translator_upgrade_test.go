package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	translator "github.com/dillonzq/api-translator"
	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
)

// TestResponsesReasoningRoundTrip exercises the plugin's response wrappers and
// native cleanup before replaying two tool rounds to a Chat upstream.
func TestResponsesReasoningRoundTrip(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var history []json.RawMessage
			want := map[string]string{}
			for round := 1; round <= 2; round++ {
				call := fmt.Sprintf("c%d", round)
				thought := fmt.Sprintf(" round %d\n思考🙂 ", round)
				want[call] = thought
				state := translator.NewRequestState()
				if _, err := buildUpstreamRequest(catalog.RouteChatCompletions, "deepseek-test", "openai-response", mustJSON(map[string]any{"input": history}), nil, state); err != nil {
					t.Fatal(err)
				}
				message := map[string]any{"role": "assistant", "content": "checking", "reasoning_content": thought,
					"tool_calls": []any{map[string]any{"index": 0, "id": call, "type": "function", "function": map[string]any{"name": "f", "arguments": "{}"}}}}
				choice := map[string]any{"message": message, "finish_reason": "tool_calls"}
				response := map[string]any{"id": fmt.Sprintf("r%d", round), "choices": []any{choice}}
				var output []json.RawMessage
				if streaming {
					delete(choice, "message")
					choice["delta"] = message
					wire := "data: " + string(mustJSON(response)) + "\n\ndata: [DONE]\n\n"
					output = completedStreamOutput(t, catalog.RouteChatCompletions, wire, state)
				} else {
					out, err := convertNonStream(catalog.RouteChatCompletions, "openai-response", 200, mustJSON(response), state)
					if err != nil {
						t.Fatal(err)
					}
					var doc struct{ Output []json.RawMessage }
					if err := json.Unmarshal(out, &doc); err != nil {
						t.Fatal(err)
					}
					output = doc.Output
				}
				history = append(history, output...)
				history = append(history, mustJSON(map[string]any{"type": "function_call_output", "call_id": call, "output": "ok"}))
				// A non-GPT native Responses hop must retain synthesized replay content.
				native, err := buildUpstreamRequest(catalog.RouteResponses, "deepseek-test", "openai-response", mustJSON(map[string]any{"input": history}), nil)
				if err != nil {
					t.Fatal(err)
				}
				replay, err := buildUpstreamRequest(catalog.RouteChatCompletions, "deepseek-test", "openai-response", native, nil)
				if err != nil {
					t.Fatal(err)
				}
				assertReasoningCalls(t, replay, want)
			}
		})
	}
}

// completedStreamOutput verifies that bytewise SSE produces replayable item
// completions identical to the final response snapshot through CPA's wrapper.
func completedStreamOutput(t *testing.T, route catalog.Route, wire string, state *translator.RequestState) []json.RawMessage {
	t.Helper()
	sc, err := newStreamConverter(route, "openai-response", state)
	if err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	done := false
	for _, b := range []byte(wire) {
		part, terminal, err := sc.Feed([]byte{b})
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, part...)
		done = done || terminal
	}
	tail, err := sc.Finish()
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	frames = append(frames, tail...)
	var decoder translator.SSEDecoder
	var items, output []json.RawMessage
	for _, event := range decoder.Feed(bytes.Join(frames, nil)) {
		var doc struct {
			Item     json.RawMessage
			Response struct{ Output []json.RawMessage }
		}
		if err := json.Unmarshal(event.Data, &doc); err != nil {
			t.Fatal(err)
		}
		switch event.Event {
		case "response.output_item.done":
			items = append(items, doc.Item)
		case "response.completed":
			output = doc.Response.Output
		}
	}
	if len(items) == 0 || !reflect.DeepEqual(items, output) {
		t.Fatalf("completed items differ from final output: %s / %s", mustJSON(items), mustJSON(output))
	}
	return output
}

// TestLocalShellRestoration verifies that native normalization registers shell
// identities and that ordinary and streaming responses reuse the same state.
func TestLocalShellRestoration(t *testing.T) {
	const request = `{"input":[{"type":"shell_call","call_id":"c","action":{"commands":["pwd"]}},{"type":"shell_call_output","call_id":"c","output":[{"stdout":"/tmp","stderr":"","outcome":{"type":"exit","exit_code":0}}]}],"tools":[{"type":"shell","environment":{"type":"local","skills":[]}}],"tool_choice":{"type":"shell"}}`
	const response = `{"id":"r","object":"response","status":"completed","output":[{"id":"fc","type":"function_call","call_id":"c","name":"shell","arguments":"{\"commands\":[\"pwd\"]}","status":"completed"}]}`
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			state := translator.NewRequestState()
			out, err := buildUpstreamRequest(catalog.RouteResponses, "deepseek-test", "openai-response", []byte(request), nil, state)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(out, []byte(`"type":"function_call"`)) || bytes.Contains(out, []byte(`"type":"shell"`)) {
				t.Fatalf("native shell was not normalized: %s", out)
			}
			var upstream struct{ Output []json.RawMessage }
			if err := json.Unmarshal([]byte(response), &upstream); err != nil {
				t.Fatal(err)
			}
			var output []json.RawMessage
			if streaming {
				wire := "event: response.output_item.done\ndata: " + string(mustJSON(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": upstream.Output[0]})) + "\n\n" +
					"event: response.completed\ndata: " + string(mustJSON(map[string]any{"type": "response.completed", "response": json.RawMessage(response)})) + "\n\n"
				output = completedStreamOutput(t, catalog.RouteResponses, wire, state)
			} else {
				converted, err := convertNonStream(catalog.RouteResponses, "openai-response", 200, []byte(response), state)
				if err != nil {
					t.Fatal(err)
				}
				var doc struct{ Output []json.RawMessage }
				if err := json.Unmarshal(converted, &doc); err != nil {
					t.Fatal(err)
				}
				output = doc.Output
			}
			var item struct {
				ID, Type, Status string
				CallID           string `json:"call_id"`
				Environment      struct{ Type string }
				Action           struct{ Commands []string }
			}
			if len(output) != 1 {
				t.Fatalf("unexpected output: %s", mustJSON(output))
			}
			if err := json.Unmarshal(output[0], &item); err != nil {
				t.Fatal(err)
			}
			if item.ID != "fc" || item.Type != "shell_call" || item.CallID != "c" || item.Status != "completed" || item.Environment.Type != "local" || !reflect.DeepEqual(item.Action.Commands, []string{"pwd"}) {
				t.Fatalf("shell identity lost: %s", output[0])
			}
		})
	}
}

// TestMediaAndCacheRequestConversion exercises new translator mappings through
// the plugin's format selection and native compatibility boundary.
func TestMediaAndCacheRequestConversion(t *testing.T) {
	for _, tc := range []struct {
		name, source, body, want string
		route                    catalog.Route
	}{
		{"mixed media to responses", "openai", `{"messages":[{"role":"user","content":[{"type":"text","text":"inspect"},{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERg=="}},{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}},{"type":"video_url","video_url":{"url":"https://example.invalid/v.mp4"}}]}]}`,
			`[{"type":"input_text","text":"inspect"},{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERg=="},{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}},{"type":"input_video","video_url":{"url":"https://example.invalid/v.mp4"}}]`, catalog.RouteResponses},
		{"mixed media to chat", "openai-response", `{"input":[{"role":"user","content":[{"type":"input_text","text":"inspect"},{"type":"input_file","file_id":"file_example"},{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}},{"type":"input_video","video_url":{"url":"https://example.invalid/v.mp4"}}]}]}`,
			`[{"type":"text","text":"inspect"},{"type":"file","file":{"file_id":"file_example"}},{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}},{"type":"video_url","video_url":{"url":"https://example.invalid/v.mp4"}}]`, catalog.RouteChatCompletions},
		{"documents and cache to messages", "openai-response", `{"input":[{"role":"user","cache_control":{"type":"ephemeral"},"content":[{"type":"input_file","file_data":"data:application/pdf;base64,JVBERg==","filename":"a.pdf","cache_control":{"type":"ephemeral","ttl":"1h","extension":9007199254740993}},{"type":"input_file","file_data":"data:text/plain;base64,aGk=","filename":"a.txt"}]}]}`,
			`[{"type":"document","title":"a.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERg=="},"cache_control":{"type":"ephemeral","ttl":"1h","extension":9007199254740993}},{"type":"document","title":"a.txt","source":{"type":"text","media_type":"text/plain","data":"hi"},"cache_control":{"type":"ephemeral"}}]`, catalog.RouteMessages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := translator.NewRequestState()
			state.Options.LossPolicy = translator.LossPolicyStrict
			out, err := buildUpstreamRequest(tc.route, "upstream", tc.source, []byte(tc.body), nil, state)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Model    string
				Messages []struct{ Content json.RawMessage }
				Input    []struct{ Content json.RawMessage }
			}
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatal(err)
			}
			messages := doc.Messages
			if tc.route == catalog.RouteResponses {
				messages = doc.Input
			}
			if doc.Model != "upstream" || len(messages) != 1 || len(state.Diagnostics()) != 0 {
				t.Fatalf("unexpected request: %s; diagnostics: %+v", out, state.Diagnostics())
			}
			decode := func(raw []byte) any {
				t.Helper()
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				var value any
				if err := decoder.Decode(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			if !reflect.DeepEqual(decode(messages[0].Content), decode([]byte(tc.want))) {
				t.Fatalf("media order or cache hint changed: %s", out)
			}
		})
	}
}
