package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	translator "github.com/dillonzq/api-translator"
	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/config"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func TestNativeUpstreamPolicies(t *testing.T) {
	for _, tc := range []struct {
		name                string
		route               catalog.Route
		model, source, body string
		contains, excludes  []string
	}{
		{"chat", catalog.RouteChatCompletions, "glm-5.3", "openai", `{"messages":[{"role":"developer","content":"system"},{"role":"user","content":"hi"}],"thinking":{},"future":9007199254740993}`, []string{`"role":"system"`, `9007199254740993`}, []string{`"developer"`, `"thinking"`}},
		{"messages string", catalog.RouteMessages, "minimax-m3", "claude", `{"system":"top","messages":[{"role":"system","content":[{"type":"thinking","thinking":"omit"},{"type":"text","text":"history"}]},{"role":"user","content":"hi"}],"future":9007199254740993}`, []string{`"system":"top\n\nhistory"`, `9007199254740993`}, []string{`"role":"system"`, `"omit"`}},
		{"messages blocks", catalog.RouteMessages, "minimax-m3", "claude", `{"system":[{"type":"text","text":"top","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"system","content":"history"}]}`, []string{`"cache_control"`, `"text":"history"`}, []string{`"role":"system"`}},
		{"gpt passthrough", catalog.RouteResponses, "gpt-6-luna", "openai-response", `{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"compaction","data":"keep"}],"tools":[{"type":"custom","name":"apply_patch"}],"future":9007199254740993}`, []string{`"reasoning"`, `"compaction"`, `"custom"`, `9007199254740993`}, nil},
		{"non gpt", catalog.RouteResponses, "muse-spark", "openai-response", `{"input":[{"type":"reasoning","encrypted_content":"omit"},{"type":"compaction"},{"type":"additional_tools","tools":[{"type":"function","name":"extra"}]},{"type":"function_call","name":"extra","call_id":"c","arguments":"null","extension":9007199254740993},{"type":"web_search_call","action":{"query":"keep"}}],"tools":[{"type":"custom","name":"apply_patch"},{"type":"tool_search"},{"type":"image_generation"},{"type":"web_search","filters":{"allowed_domains":["omit"]}},{"type":"namespace","name":"ns","tools":[{"type":"custom","name":"run"}]}]}`, []string{`"name":"extra"`, `"arguments":"{}"`, `9007199254740993`, `"query":"keep"`, `"name":"ns__run"`, `"type":"web_search"`}, []string{`"reasoning"`, `"compaction"`, `"additional_tools"`, `"apply_patch"`, `"tool_search"`, `"image_generation"`, `"allowed_domains"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := buildUpstreamRequest(tc.route, tc.model, tc.source, []byte(tc.body), nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range tc.contains {
				if !strings.Contains(string(out), v) {
					t.Fatalf("missing %s: %s", v, out)
				}
			}
			for _, v := range tc.excludes {
				if strings.Contains(string(out), v) {
					t.Fatalf("unexpected %s: %s", v, out)
				}
			}
		})
	}
	for _, body := range []string{`null`, `{"messages":[{"role":"system","content":[{"type":"image"}]}]}`, `{"messages":[{"role":"system","content":[{"type":"future"}]}]}`, `{"messages":[{"role":"system","content":"hi"}],"system":42}`} {
		if _, err := buildUpstreamRequest(catalog.RouteMessages, "m", "claude", []byte(body), nil); err == nil {
			t.Fatalf("invalid native request accepted: %s", body)
		}
	}
}

func TestNativeToolStateIsolation(t *testing.T) {
	// Both requests use the same flattened name for different original identities.
	var wg sync.WaitGroup
	for _, custom := range []bool{false, true} {
		wg.Add(1)
		go func(custom bool) {
			defer wg.Done()
			typ, ns, name := "function", "", "ns__run"
			if custom {
				typ, ns, name = "custom", "ns", "run"
			}
			declaration := fmt.Sprintf(`{"type":%q,"name":%q}`, typ, name)
			if custom {
				declaration = fmt.Sprintf(`{"type":"namespace","name":"ns","tools":[%s]}`, declaration)
			}
			state := translator.NewRequestState()
			request := []byte(`{"input":"hi","tools":[` + declaration + `]}`)
			if _, err := buildUpstreamRequest(catalog.RouteResponses, "muse", "openai-response", request, nil, state); err != nil {
				t.Error(err)
				return
			}
			response := []byte(`{"id":"r","object":"response","status":"completed","output":[{"id":"fc","type":"function_call","call_id":"c","name":"ns__run","arguments":"{\"input\":\"pwd\"}"}]}`)
			out, err := convertNonStream(catalog.RouteResponses, "openai-response", 200, response, state)
			if err != nil {
				t.Error(err)
				return
			}
			var doc struct {
				Output []struct{ Type, Name, Namespace, Input string }
			}
			if json.Unmarshal(out, &doc) != nil || len(doc.Output) != 1 {
				t.Errorf("response=%s", out)
				return
			}
			expectedType := "function_call"
			if custom {
				expectedType = "custom_tool_call"
			}
			if got := doc.Output[0]; got.Type != expectedType || got.Name != name || got.Namespace != ns {
				t.Errorf("state crossed requests: %+v", got)
			}
			sc, err := newStreamConverter(catalog.RouteResponses, "openai-response", state)
			if err != nil {
				t.Error(err)
				return
			}
			frame := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + string(response) + "}\n\n")
			events, done, err := sc.Feed(frame)
			joined := bytes.Join(events, nil)
			if err != nil || !done || !bytes.Contains(joined, []byte(expectedType)) || custom && !bytes.Contains(joined, []byte(`"namespace":"ns"`)) {
				t.Errorf("stream=%s done=%v err=%v", joined, done, err)
			}
		}(custom)
	}
	wg.Wait()
}

func TestNativeNormalizationRejectsCollisions(t *testing.T) {
	state := translator.NewRequestState()
	body := []byte(`{"tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"run"}]},{"type":"function","name":"ns__run"}]}`)
	if _, err := buildUpstreamRequest(catalog.RouteResponses, "muse", "openai-response", body, nil, state); err == nil {
		t.Fatal("collision accepted")
	}
	response := []byte(`{"output":[{"type":"function_call","name":"ns__run","arguments":"{}"}]}`)
	out, err := convertNonStream(catalog.RouteResponses, "openai-response", 200, response, state)
	if err != nil || !bytes.Equal(out, response) {
		t.Fatalf("failed preparation committed identities: %s %v", out, err)
	}
}

type errorTailConverter struct{ finish bool }

func (c errorTailConverter) Feed([]byte) ([][]byte, bool, *errclass.Error) {
	if c.finish {
		return nil, false, nil
	}
	return [][]byte{[]byte(`{"delta":"before-error"}`)}, false, errclass.Translation("conversion failed")
}
func (c errorTailConverter) Finish() ([][]byte, *errclass.Error) {
	return [][]byte{[]byte(`{"delta":"before-error"}`)}, errclass.Translation("finish failed")
}

func TestPumpEmitsEventsBeforeConversionError(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(fmt.Sprint(finish), func(t *testing.T) {
			f := &fakeCaller{responder: streamResponder(streamScript{frames: []string{"payload"}})}
			m := NewManager(NewHostBridge(f.call))
			m.pumpStreamContext(context.Background(), "down", "up", &resolvedExecution{cfg: config.Config{MaxResponseBytes: 1024}}, errorTailConverter{finish: finish})
			emits := f.callsOf(pluginabi.MethodHostStreamEmit)
			if len(emits) != 1 {
				t.Fatalf("events discarded: %+v", emits)
			}
			payload := wireBody(t, decodePayload(t, emits[0]), "payload")
			if string(payload) != `{"delta":"before-error"}` {
				t.Fatalf("wrong event: %s", payload)
			}
			closes := f.callsOf(pluginabi.MethodHostStreamClose)
			if len(closes) != 1 || !bytes.Contains(closes[0].payload, []byte("failed")) {
				t.Fatalf("error close=%+v", closes)
			}
		})
	}
}

func TestInvalidOutputRejectedBeforeHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		m, f := newExecManager(t)
		before := len(f.callsOf(pluginabi.MethodHostHTTPDo)) + len(f.callsOf(pluginabi.MethodHostHTTPDoStream))
		req := executorRequest{}
		json.Unmarshal(execReqBody("opencode-go/glm-5.3", "openai", []byte(ccRequestBody), stream), &req)
		req.Format = "unknown"
		wire, _ := json.Marshal(req)
		out, err := m.handleExecute(wire)
		if err != nil || decodeEnv(t, out).OK {
			t.Fatalf("invalid output accepted: %s %v", out, err)
		}
		if len(f.callsOf(pluginabi.MethodHostHTTPDo))+len(f.callsOf(pluginabi.MethodHostHTTPDoStream)) != before {
			t.Fatal("invalid output reached upstream")
		}
	}
}

// Native thinking sanitization is provider policy, so these cases stay here
// after removing the generic adapters. Both execution modes share this builder.
func TestNativeChatThinkingSanitization(t *testing.T) {
	for _, model := range []string{"other", "m"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct{ name, thinking string }{
				{"string", `"fast"`}, {"number", `123`}, {"array", `["low"]`}, {"boolean", `true`}, {"null", `null`},
				{"numeric type", `{"type":123}`}, {"null type", `{"type":null}`}, {"array type", `{"type":["enabled"]}`},
				{"empty type", `{"type":""}`}, {"spaces", `{"type":"   "}`}, {"tabs and newlines", `{"type":"\t\n\r"}`},
				{"unicode whitespace", `{"type":"　\u00a0"}`}, {"empty object", `{}`}, {"metadata", `{"levels":["low","high"]}`},
			} {
				t.Run(fmt.Sprintf("%s/%t/%s", model, stream, tc.name), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"model":%q,"thinking":%s,"stream":%t,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}],"n":9007199254740993}`, model, tc.thinking, stream))
					out, err := buildUpstreamRequest(catalog.RouteChatCompletions, "m", "openai", body, nil)
					if err != nil {
						t.Fatal(err)
					}
					var doc map[string]json.RawMessage
					if json.Unmarshal(out, &doc) != nil {
						t.Fatalf("invalid request=%s", out)
					}
					if _, exists := doc["thinking"]; exists {
						t.Fatalf("invalid thinking forwarded: %s", out)
					}
					if string(doc["model"]) != `"m"` || string(doc["stream"]) != fmt.Sprint(stream) || string(doc["reasoning_effort"]) != `"high"` || string(doc["n"]) != "9007199254740993" {
						t.Fatalf("unrelated fields changed: %s", out)
					}
				})
			}
			for _, thinking := range []string{`{"type":"enabled","budget_tokens":1024}`, `{"type":"disabled"}`, `{"type":" enabled ","future":9007199254740993}`} {
				body := []byte(fmt.Sprintf(`{"model":%q,"thinking":%s,"stream":%t}`, model, thinking, stream))
				out, err := buildUpstreamRequest(catalog.RouteChatCompletions, "m", "openai", body, nil)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]json.RawMessage
				json.Unmarshal(out, &doc)
				if string(doc["thinking"]) != thinking || model == "m" && !bytes.Equal(body, out) {
					t.Fatalf("valid thinking changed: %s", out)
				}
			}
		}
	}
	for _, body := range []string{"", `null`, `123`, `"string"`, `[]`, `true`} {
		if _, err := buildUpstreamRequest(catalog.RouteChatCompletions, "m", "openai", []byte(body), nil); err == nil || err.Class != errclass.ClassTranslation {
			t.Fatalf("invalid root accepted: %q %v", body, err)
		}
	}
}

func TestNativeCustomToolIncrementalLifecycle(t *testing.T) {
	const input = "printf \"你好🙂\"\nnext\t\\path"
	const callID = "call_new"
	const itemID = "fc_new"
	request := []byte(`{
  "model":"client","metadata":{"n":9007199254740993},
  "tools":[{"type":"namespace","name":"shell","tools":[{"type":"custom","name":"run"}]},{"type":"custom","name":"apply_patch"}],
  "input":[
   {"type":"reasoning","encrypted_content":"omit"},
   {"type":"custom_tool_call","name":"run","namespace":"shell","call_id":"call_old","input":"echo history","extension":9007199254740993},
   {"type":"custom_tool_call_output","call_id":"call_old","output":[{"type":"input_text","text":"history result"},{"type":"input_image","image_url":"https://example.com/history.png"}]},
   {"role":"user","content":"continue"}
  ],"tool_choice":{"type":"custom","name":"run","namespace":"shell"}
 }`)
	// Every chunking run prepares its own state through the plugin's native policy.
	for _, chunkSize := range []int{1, 7, 4096} {
		t.Run(fmt.Sprint(chunkSize), func(t *testing.T) {
			state := translator.NewRequestState()
			prepared, err := buildUpstreamRequest(catalog.RouteResponses, "muse-spark-1.3-contributor", "openai-response", request, nil, state)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Model      string
				Metadata   map[string]json.RawMessage
				Tools      []struct{ Type, Name string }
				Input      []map[string]json.RawMessage
				ToolChoice struct{ Type, Name, Namespace string } `json:"tool_choice"`
			}
			if err := json.Unmarshal(prepared, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Model != "muse-spark-1.3-contributor" || len(doc.Tools) != 1 || doc.Tools[0].Type != "function" || doc.Tools[0].Name != "shell__run" || doc.ToolChoice.Type != "function" || doc.ToolChoice.Name != "shell__run" || doc.ToolChoice.Namespace != "" {
				t.Fatalf("native policy not applied: %s", prepared)
			}
			if len(doc.Input) != 3 || sessionString(doc.Input[0]["type"]) != "function_call" || sessionString(doc.Input[0]["name"]) != "shell__run" || sessionString(doc.Input[0]["call_id"]) != "call_old" || hasContent(doc.Input[0]["namespace"]) || hasContent(doc.Input[0]["input"]) {
				t.Fatalf("history not normalized: %s", prepared)
			}
			var historicalArgs map[string]string
			if json.Unmarshal([]byte(sessionString(doc.Input[0]["arguments"])), &historicalArgs) != nil || historicalArgs["input"] != "echo history" || string(doc.Input[0]["extension"]) != "9007199254740993" || string(doc.Metadata["n"]) != "9007199254740993" {
				t.Fatalf("history input/extension changed: %s", prepared)
			}
			if sessionString(doc.Input[1]["type"]) != "function_call_output" || sessionString(doc.Input[1]["call_id"]) != "call_old" || !bytes.Contains(doc.Input[1]["output"], []byte("history.png")) {
				t.Fatalf("history result lost: %s", prepared)
			}
			var results []struct {
				Type, Text string
				ImageURL   string `json:"image_url"`
			}
			if json.Unmarshal(doc.Input[1]["output"], &results) != nil || len(results) != 2 || results[0].Type != "input_text" || results[0].Text != "history result" || results[1].Type != "input_image" || results[1].ImageURL != "https://example.com/history.png" {
				t.Fatalf("history result order/content changed: %s", prepared)
			}

			args, _ := json.Marshal(map[string]string{"input": input})
			item := map[string]any{"type": "function_call", "id": itemID, "call_id": callID, "name": "shell__run", "arguments": string(args), "status": "completed"}
			response := map[string]any{"id": "r", "object": "response", "status": "completed", "output": []any{item}}
			responseBody, _ := json.Marshal(response)
			restored, err := convertNonStream(catalog.RouteResponses, "openai-response", 200, responseBody, state)
			if err != nil {
				t.Fatal(err)
			}
			assertItem := func(raw json.RawMessage, wantInput string) {
				t.Helper()
				var got map[string]json.RawMessage
				if json.Unmarshal(raw, &got) != nil {
					t.Fatalf("invalid item: %s", raw)
				}
				if sessionString(got["type"]) != "custom_tool_call" || sessionString(got["name"]) != "run" || sessionString(got["namespace"]) != "shell" || sessionString(got["id"]) != itemID || sessionString(got["call_id"]) != callID || sessionString(got["input"]) != wantInput || hasContent(got["arguments"]) {
					t.Fatalf("item identity/input changed: %s", raw)
				}
			}
			var regular struct{ Output []json.RawMessage }
			if json.Unmarshal(restored, &regular) != nil || len(regular.Output) != 1 {
				t.Fatalf("regular response=%s", restored)
			}
			assertItem(regular.Output[0], input)

			var wire strings.Builder
			frame := func(name string, payload map[string]any) {
				payload["type"] = name
				body, _ := json.Marshal(payload)
				wire.WriteString("event: " + name + "\ndata: " + string(body) + "\n\n")
			}
			frame("response.created", map[string]any{"response": map[string]any{"id": "r", "model": "muse-spark-1.3-contributor"}})
			frame("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": itemID, "call_id": callID, "name": "shell__run", "arguments": ""}})
			// Split the function wrapper and its escaped string over distinct argument events.
			argumentRunes := []rune(string(args))
			for start := 0; start < len(argumentRunes); start += 5 {
				end := min(start+5, len(argumentRunes))
				frame("response.function_call_arguments.delta", map[string]any{"item_id": itemID, "output_index": 0, "delta": string(argumentRunes[start:end])})
			}
			frame("response.function_call_arguments.done", map[string]any{"item_id": itemID, "output_index": 0, "name": "shell__run", "arguments": string(args)})
			frame("response.output_item.done", map[string]any{"output_index": 0, "item": item})
			frame("response.completed", map[string]any{"response": response})
			sc, err := newStreamConverter(catalog.RouteResponses, "openai-response", state)
			if err != nil {
				t.Fatal(err)
			}
			encoded := []byte(wire.String())
			var events [][]byte
			done := false
			for start := 0; start < len(encoded); start += chunkSize {
				end := min(start+chunkSize, len(encoded))
				out, terminal, err := sc.Feed(encoded[start:end])
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, out...)
				done = done || terminal
			}
			if !done {
				t.Fatal("valid stream did not finish")
			}
			tail, err := sc.Finish()
			if err != nil || len(tail) != 0 {
				t.Fatalf("Finish repeated terminal: %q %v", tail, err)
			}
			repeated, done, err := sc.Feed(encoded)
			if err != nil || !done || len(repeated) != 0 {
				t.Fatalf("completed stream emitted again: %q %v", repeated, err)
			}
			var decoder translator.SSEDecoder
			counts := make(map[string]int)
			var inputDeltas strings.Builder
			for _, out := range events {
				for _, ev := range decoder.Feed(out) {
					var payload map[string]json.RawMessage
					if json.Unmarshal(ev.Data, &payload) != nil || sessionString(payload["type"]) != ev.Event {
						t.Fatalf("invalid event=%s", ev.Data)
					}
					counts[ev.Event]++
					switch ev.Event {
					case "response.output_item.added":
						assertItem(payload["item"], "")
					case "response.custom_tool_call_input.delta":
						if sessionString(payload["item_id"]) != itemID {
							t.Fatalf("delta identity=%s", ev.Data)
						}
						inputDeltas.WriteString(sessionString(payload["delta"]))
					case "response.custom_tool_call_input.done":
						if sessionString(payload["item_id"]) != itemID || sessionString(payload["name"]) != "run" || sessionString(payload["namespace"]) != "shell" || sessionString(payload["input"]) != input || hasContent(payload["arguments"]) {
							t.Fatalf("done identity/input=%s", ev.Data)
						}
					case "response.output_item.done":
						assertItem(payload["item"], input)
					case "response.completed":
						var terminal struct{ Output []json.RawMessage }
						if json.Unmarshal(payload["response"], &terminal) != nil || len(terminal.Output) != 1 {
							t.Fatalf("terminal output duplicated/missing: %s", ev.Data)
						}
						assertItem(terminal.Output[0], input)
					case "response.function_call_arguments.delta", "response.function_call_arguments.done":
						t.Fatalf("function wrapper leaked: %s", ev.Data)
					}
				}
			}
			if inputDeltas.String() != input {
				t.Fatalf("input deltas duplicated/lost: %q, want %q", inputDeltas.String(), input)
			}
			for _, name := range []string{"response.created", "response.output_item.added", "response.custom_tool_call_input.done", "response.output_item.done", "response.completed"} {
				if counts[name] != 1 {
					t.Fatalf("%s count=%d", name, counts[name])
				}
			}
			if counts["response.custom_tool_call_input.delta"] == 0 {
				t.Fatal("wrapped argument deltas did not produce a custom input delta")
			}
		})
	}
}
