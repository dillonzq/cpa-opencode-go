package plugin

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	translator "github.com/dillonzq/api-translator"
	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
)

// TestNativeResponsesReasoningReplay preserves original parts, summaries,
// extension fields, and the ordering of reasoning and tool calls.
func TestNativeResponsesReasoningReplay(t *testing.T) {
	for _, model := range []string{"deepseek-v4.1-flash", "muse-spark"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"input":[{"type":"reasoning","id":"r1","content":[{"type":"reasoning_text","text":" raw one "}],"summary":[{"type":"summary_text","text":"display"}],"encrypted_content":"foreign","extension":9007199254740993},{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"},{"type":"reasoning","id":"r2","content":[{"type":"reasoning_text","text":"raw two"}]},{"type":"function_call","call_id":"c2","name":"f","arguments":"{}"},{"type":"reasoning","id":"summary-only","summary":[{"type":"summary_text","text":"not original"}]},{"type":"reasoning","encrypted_content":"foreign"},{"type":"compaction"}]}`)
			out, err := buildUpstreamRequest(catalog.RouteResponses, model, "openai-response", body, nil)
			if err != nil {
				t.Fatal(err)
			}
			var before, after struct {
				Input []map[string]json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(body, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(out, &after); err != nil {
				t.Fatal(err)
			}
			if len(after.Input) != 6 {
				t.Fatalf("history length changed: %s", out)
			}
			delete(before.Input[0], "encrypted_content")
			for i := range after.Input {
				if !reflect.DeepEqual(before.Input[i], after.Input[i]) {
					t.Fatalf("history item %d changed: %s", i, out)
				}
			}
			if _, ok := after.Input[5]["content"]; ok {
				t.Fatalf("summary promoted to original content: %s", out)
			}
		})
	}
}

// TestNativeResponsesEmptyReasoning drops history without meaningful typed
// reasoning, while preserving retained items and the GPT passthrough policy.
func TestNativeResponsesEmptyReasoning(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		keep         bool
	}{
		{"absent", ``, false},
		{"null", `,"content":null,"summary":null`, false},
		{"empty summary", `,"summary":[]`, false},
		{"empty content", `,"content":[]`, false},
		{"empty both", `,"content": [ ],"summary":[]`, false},
		{"null content empty summary", `,"content":null,"summary":[]`, false},
		{"empty content null summary", `,"content":[],"summary":null`, false},
		{"null parts", `,"content":[null],"summary":[null]`, false},
		{"scalar parts", `,"content":[""],"summary":[42,false]`, false},
		{"object fields", `,"content":{"type":"reasoning_text","text":"raw"},"summary":{}`, false},
		{"scalar fields", `,"content":"raw","summary":true`, false},
		{"missing type", `,"content":[{"text":"raw"}],"summary":[{"text":"display"}]`, false},
		{"wrong types", `,"content":[{"type":"summary_text","text":"display"}],"summary":[{"type":"reasoning_text","text":"raw"}]`, false},
		{"unknown type", `,"content":[{"type":"future","text":"unknown"}]`, false},
		{"missing text", `,"content":[{"type":"reasoning_text"}],"summary":[{"type":"summary_text"}]`, false},
		{"null text", `,"content":[{"type":"reasoning_text","text":null}],"summary":[{"type":"summary_text","text":null}]`, false},
		{"invalid text", `,"content":[{"type":"reasoning_text","text":42}],"summary":[{"type":"summary_text","text":false}]`, false},
		{"empty text", `,"content":[{"type":"reasoning_text","text":""}],"summary":[{"type":"summary_text","text":""}]`, false},
		{"whitespace text", `,"content":[{"type":"reasoning_text","text":" \t\n"}],"summary":[{"type":"summary_text","text":"\u3000\u00a0"}]`, false},
		{"content only", `,"content":[{"type":"reasoning_text","text":" raw "}],"summary":[]`, true},
		{"summary only", `,"content":[],"summary":[{"type":"summary_text","text":"display"}]`, true},
		{"unicode content", `,"content":[{"type":"reasoning_text","text":" \t思考🙂\n","extension":9007199254740993}]`, true},
		{"later readable part", `,"content":[{"type":"reasoning_text","text":" "},{"type":"reasoning_text","text":"raw"}]`, true},
		{"readable content blank summary", `,"content":[{"type":"reasoning_text","text":"raw"}],"summary":[{"type":"summary_text","text":" "}]`, true},
		{"blank content readable summary", `,"content":[{"type":"reasoning_text","text":" "}],"summary":[{"type":"summary_text","text":"display"}]`, true},
	} {
		for _, model := range []string{"deepseek-v4.1-flash", "GPT-6"} {
			t.Run(tc.name+"/"+model, func(t *testing.T) {
				body := []byte(`{"input":[{"type":"reasoning","id":"r","encrypted_content":"foreign"` + tc.fields + `},{"role":"user","content":"next"}]}`)
				out, err := buildUpstreamRequest(catalog.RouteResponses, model, "openai-response", body, nil)
				if err != nil {
					t.Fatal(err)
				}
				var doc struct{ Input []map[string]json.RawMessage }
				if err := json.Unmarshal(out, &doc); err != nil {
					t.Fatal(err)
				}
				want := 1
				if tc.keep || model == "GPT-6" {
					want = 2
				}
				if len(doc.Input) != want || sessionString(doc.Input[want-1]["role"]) != "user" {
					t.Fatalf("unexpected history: %s", out)
				}
				if want == 2 {
					var original struct{ Input []map[string]json.RawMessage }
					if err := json.Unmarshal(body, &original); err != nil {
						t.Fatal(err)
					}
					if model != "GPT-6" {
						delete(original.Input[0], "encrypted_content")
					}
					if !bytes.Equal(mustJSON(doc.Input[0]), mustJSON(original.Input[0])) {
						t.Fatalf("reasoning parts changed: %s", out)
					}
				}
			})
		}
	}
}

// TestReasoningHistoryCrossProtocol keeps distinct reasoning rounds attached to
// parallel calls and verifies the default Messages unsigned-thinking boundary.
func TestReasoningHistoryCrossProtocol(t *testing.T) {
	for _, source := range []struct{ format, body string }{
		{"openai", `{"messages":[{"role":"assistant","content":"checking","reasoning_content":" raw one🙂 ","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"A"},{"role":"tool","tool_call_id":"b","content":"B"},{"role":"assistant","reasoning_content":"raw two","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`},
		{"claude", `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":" raw one🙂 ","signature":"foreign"},{"type":"text","text":"checking"},{"type":"tool_use","id":"a","name":"f","input":{}},{"type":"tool_use","id":"b","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"},{"type":"tool_result","tool_use_id":"b","content":"B"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"raw two"},{"type":"tool_use","id":"c","name":"f","input":{}}]}]}`},
		{"openai-response", `{"input":[{"type":"reasoning","content":[{"type":"reasoning_text","text":" raw one🙂 "}],"summary":[{"type":"summary_text","text":"display only"}]},{"role":"assistant","content":[{"type":"output_text","text":"checking"}]},{"type":"function_call","call_id":"a","name":"f","arguments":"{}"},{"type":"function_call","call_id":"b","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"A"},{"type":"function_call_output","call_id":"b","output":"B"},{"type":"reasoning","summary":[{"type":"summary_text","text":"never original"}]},{"type":"reasoning","content":[{"type":"reasoning_text","text":"raw two"}]},{"type":"function_call","call_id":"c","name":"f","arguments":"{}"}]}`},
	} {
		t.Run(source.format, func(t *testing.T) {
			// Exercise the plugin's native cleanup as well as cross-protocol replay.
			responses, err := buildUpstreamRequest(catalog.RouteResponses, "deepseek-test", source.format, []byte(source.body), nil)
			if err != nil {
				t.Fatal(err)
			}
			chat, err := buildUpstreamRequest(catalog.RouteChatCompletions, "deepseek-test", "openai-response", responses, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertReasoningCalls(t, chat, map[string]string{"a": " raw one🙂 ", "b": " raw one🙂 ", "c": "raw two"})
			state := translator.NewRequestState()
			messages, err := buildUpstreamRequest(catalog.RouteMessages, "messages-test", "openai-response", responses, nil, state)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Messages []struct{ Content []struct{ Type string } }
			}
			if err := json.Unmarshal(messages, &doc); err != nil {
				t.Fatal(err)
			}
			for _, message := range doc.Messages {
				for _, part := range message.Content {
					if part.Type == "thinking" {
						t.Fatalf("unsigned thinking sent by default: %s", messages)
					}
				}
			}
			found := false
			for _, diagnostic := range state.Diagnostics() {
				found = found || diagnostic.Code == "unsigned_thinking"
			}
			if !found {
				t.Fatalf("missing signature loss diagnostic: %+v", state.Diagnostics())
			}
		})
	}
}

func assertReasoningCalls(t *testing.T, body []byte, want map[string]string) {
	t.Helper()
	var doc struct {
		Messages []struct {
			Reasoning string                `json:"reasoning_content"`
			Calls     []struct{ ID string } `json:"tool_calls"`
		}
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, message := range doc.Messages {
		for _, call := range message.Calls {
			got[call.ID] = message.Reasoning
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasoning detached from calls: got %v, want %v; %s", got, want, body)
	}
}
