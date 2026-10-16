package plugin

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
)

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
