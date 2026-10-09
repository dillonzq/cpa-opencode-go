package responses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dillonzq/cpa-opencode-go/internal/adapter/shared"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
)

// StreamConverter converts an upstream /v1/responses SSE stream into the
// client protocol incrementally (FR-006, AC §D; Luna REQUIRED v1 scope per
// 07-open-questions.md §5).
//
// Contract: instances are single-use and strictly sequential — Feed is
// called from one goroutine with consecutive network chunks; there is no
// internal locking. Partial SSE lines are buffered until a blank line
// completes the `event:`/`data:` pair.
type StreamConverter struct {
	done   bool
	ending bool
	framer *shared.SSEFramer
	source string

	respTools *shared.ResponseTools
	// customItems tracks upstream function_call item identifiers (both
	// "id" and "call_id") announced as custom tools, so a later
	// response.function_call_arguments.done carrying only item_id (no
	// name) still restores to custom_tool_call_input.done.
	customItems map[string]struct{}

	// Response identity captured from response.created.
	id      string
	model   string
	created int64

	// openai (Chat Completions) target state.
	toolCallsSeen bool

	// Reasoning parts track both target formats; block state is Messages-only.
	reasonIndex    int
	reasonOpen     bool
	reasoningParts map[string]*strings.Builder
	visibleParts   map[string]*strings.Builder
	refused        bool
	textIndex      int
	textOpen       bool
	nextIndex      int
	openBlocks     []int

	// Shared function_call announce-or-replay decision table for both
	// conversion targets; an instance converts to exactly one target,
	// so nextIndex doubles as the tool block index allocator.
	tracker       *toolCallTracker
	messagesTools map[int]*messagesTool
	messagesQueue []messagesEmission
}

// NewStreamConverter builds a converter for sourceFormat ("openai",
// "claude", "openai-response"); unknown formats fail on first Feed with
// ClassUnsupported (same classes as BuildRequest).
func NewStreamConverter(sourceFormat string, tools ...*shared.ResponseTools) *StreamConverter {
	sc := &StreamConverter{
		// wantRaw only for the openai-response passthrough, which
		// forwards verbatim blocks; a rebuilt single data line would
		// embed raw newlines from legally multi-data-line frames and
		// corrupt native framing. Conversion targets parse the joined
		// payload and never read raw.
		framer:      shared.NewSSEFramer(sourceFormat == "openai-response"),
		source:      sourceFormat,
		id:          "opencode-go",
		textIndex:   -1,
		respTools:   shared.ResponseToolContext(tools),
		customItems: map[string]struct{}{},
	}
	alloc := sc.allocIndex
	if sourceFormat == "claude" {
		// Tool tracker ordinals are separate from wire block indexes. Blocks
		// receive their index when emitted from the serialized Messages queue.
		ordinal := 0
		alloc = func() int { i := ordinal; ordinal++; return i }
	}
	sc.tracker = newToolCallTracker(alloc)
	return sc
}

// allocIndex hands out the next block/entry index, shared by text and
// tool blocks in Messages vocabulary and by tool entries alone in Chat
// Completions vocabulary (only one target runs per converter instance).
func (sc *StreamConverter) allocIndex() int {
	i := sc.nextIndex
	sc.nextIndex++
	return i
}

// toolCallTracker is the single announce-or-replay decision table for
// upstream function_call items, keyed by call_id and owned by both
// conversion targets so their delivery decisions cannot diverge:
// announce once per call, and deliver only a complete snapshot's missing
// suffix after any previously streamed argument fragments.
//
// OpenAI-conformant Responses streams identify one item in TWO distinct
// namespaces: output_item events carry call_id ("call_…") while
// argument-delta events reference the item's own id ("fc_…"). Every
// registration therefore binds BOTH identifiers to the same state and
// lookups resolve through either before creating new state — otherwise
// each delta fragment would fork a second tracker state and clients
// would see a ghost tool entry beside the announced call.
type toolCallTracker struct {
	calls  map[string]*toolCallState
	states []*toolCallState
	alloc  func() int
}

type toolCallState struct {
	index     int
	args      strings.Builder
	callID    string
	name      string
	complete  bool
	announced bool
	sent      int
}

func (st *toolCallState) hasIdentity() bool { return st.callID != "" && st.name != "" }

func newToolCallTracker(alloc func() int) *toolCallTracker {
	return &toolCallTracker{calls: map[string]*toolCallState{}, alloc: alloc}
}

// Observe records identity and a complete argument snapshot separately.
// An argument-only state is not a downstream announcement.
func (t *toolCallTracker) Observe(callID, itemID, name, args string, complete bool) *toolCallState {
	st := t.state(callID, itemID)
	if callID != "" {
		st.callID = callID
	}
	if name != "" {
		st.name = name
	}
	previous := st.args.String()
	if strings.HasPrefix(args, previous) {
		st.args.WriteString(args[len(previous):])
	}
	st.complete = st.complete || complete
	return st
}

func (t *toolCallTracker) StreamArgs(itemID, delta string) *toolCallState {
	st := t.state(itemID, "")
	st.args.WriteString(delta)
	return st
}

// state resolves key, then alias, binding whichever identifiers are
// present to the shared state so both namespaces stay one call.
func (t *toolCallTracker) state(key, alias string) *toolCallState {
	if st, ok := t.calls[key]; ok {
		t.bind(alias, st)
		return st
	}
	if st, ok := t.calls[alias]; ok {
		t.bind(key, st)
		return st
	}
	st := &toolCallState{index: t.alloc()}
	t.states = append(t.states, st)
	t.bind(key, st)
	t.bind(alias, st)
	return st
}

func (t *toolCallTracker) bind(key string, st *toolCallState) {
	if key != "" {
		t.calls[key] = st
	}
}

// ---- upstream Responses SSE payload shapes ----
//
// One typed struct per handled event type, keyed off the framer's event
// name; unknown keys are ignored and absent members stay zero-valued at
// no cost. Unknown EVENT types are JSON-validated without decoding and
// ignored.

type usageCounts struct {
	InputTokens  float64 `json:"input_tokens"`
	OutputTokens float64 `json:"output_tokens"`
	InputDetails *struct {
		CachedTokens     *float64 `json:"cached_tokens"`
		CacheWriteTokens *float64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails *struct {
		ReasoningTokens *float64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type errorMessage struct {
	Message string `json:"message"`
}

type responseMeta struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	CreatedAt  float64            `json:"created_at"`
	StatusCode float64            `json:"status_code"`
	Output     []functionCallItem `json:"output"`
	Usage      usageCounts        `json:"usage"`
	Error      errorMessage       `json:"error"`
}

type functionCallItem struct {
	Summary   []respTextPart `json:"summary"`
	Content   []respTextPart `json:"content"`
	Type      string         `json:"type"`
	CallID    string         `json:"call_id"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments string         `json:"arguments"`
}

type createdEvent struct {
	Response responseMeta `json:"response"`
}

type textDeltaEvent struct {
	ItemID       string `json:"item_id"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta"`
	Text         string `json:"text"`
	Refusal      string `json:"refusal"`
}

type itemEvent struct {
	Item functionCallItem `json:"item"`
}

type argsDeltaEvent struct {
	ItemID    string `json:"item_id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Delta     string `json:"delta"`
	Arguments string `json:"arguments"`
}

type terminalEvent struct {
	Response responseMeta `json:"response"`
}

type failureEvent struct {
	StatusCode float64      `json:"status_code"`
	Message    string       `json:"message"`
	Response   responseMeta `json:"response"`
}

// decodeEvent parses one SSE data payload into its typed shape; malformed
// JSON yields a translation failure carrying only a short redacted
// snippet (FR-006, §5 security: no upstream body echo). An empty payload
// decodes to zero values, matching the legacy empty-map behavior.
func decodeEvent[T any](eventType, payload string, v *T) *errclass.Error {
	if payload == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(payload), v); err != nil {
		return errclass.Translation(fmt.Sprintf(
			"malformed %s event payload: %s", eventType, shared.RedactedSnippet(payload)))
	}
	return nil
}

// checkTarget rejects unknown conversion sources after payload decode, so
// malformed-payload translation failures keep their pre-existing
// precedence over ClassUnsupported.
func (sc *StreamConverter) checkTarget() *errclass.Error {
	if sc.source == "openai" || sc.source == "claude" {
		return nil
	}
	return shared.UnsupportedFormat(sc.source, EndpointPath)
}

// Feed consumes one network chunk and returns synthesized client events,
// whether the upstream stream reached a terminal state, and a classified
// error for failed/malformed streams (FR-006). Native non-terminal frames
// are forwarded verbatim; terminal snapshots are validated before forwarding.
// Conversion targets parse eagerly into typed per-event structs.
func (sc *StreamConverter) Feed(chunk []byte) (events [][]byte, done bool, eErr *errclass.Error) {
	if sc.done {
		return nil, true, nil
	}
	sc.framer.Push(chunk)
	for {
		eventType, payload, raw, ok := sc.framer.Next()
		if !ok {
			return events, done, nil
		}
		if (eventType == "response.completed" || eventType == "response.incomplete" || sc.ending && payload != "") && !shared.IsJSONObject(payload) {
			return nil, false, errclass.Translation("Responses stream has an incomplete event payload")
		}
		if eventType == "response.completed" || eventType == "response.incomplete" {
			if eErr := validateTerminalPayload(eventType, payload); eErr != nil {
				return events, false, eErr
			}
		}
		if sc.source == "openai-response" {
			evs, d, dErr := sc.passthroughEvent(eventType, payload, raw)
			events = append(events, evs...)
			if dErr != nil {
				return events, false, dErr
			}
			if d {
				sc.done = true
				return events, true, nil
			}
			continue
		}
		evs, d, dErr := sc.convertEvent(eventType, payload)
		events = append(events, evs...)
		if dErr != nil {
			return events, false, dErr
		}
		if d {
			sc.done = true
			return events, true, nil
		}
	}
}

// A terminal event must carry a coherent response snapshot, not merely an
// arbitrary JSON object. Validate the fields that establish response identity,
// terminal status and final output before native passthrough or conversion.
// Usage and unrelated metadata may be absent; extra fields stay compatible.
func validateTerminalPayload(eventType, payload string) *errclass.Error {
	var event struct {
		Type     string `json:"type"`
		Response *struct {
			ID     string          `json:"id"`
			Object string          `json:"object"`
			Status string          `json:"status"`
			Output json.RawMessage `json:"output"`
		} `json:"response"`
	}
	fail := func() *errclass.Error {
		return errclass.Translation("Responses terminal payload requires matching type and response id, object, status, and output array")
	}
	if json.Unmarshal([]byte(payload), &event) != nil || event.Type != eventType || event.Response == nil {
		return fail()
	}
	response := event.Response
	if strings.TrimSpace(response.ID) == "" || response.Object != "response" || response.Status != strings.TrimPrefix(eventType, "response.") {
		return fail()
	}
	output := strings.TrimSpace(string(response.Output))
	if !strings.HasPrefix(output, "[") {
		return fail()
	}
	var items []json.RawMessage
	if json.Unmarshal(response.Output, &items) != nil {
		return fail()
	}
	for _, item := range items {
		if !shared.IsJSONObject(string(item)) {
			return fail()
		}
	}
	return nil
}

// Finish drains the last SSE frame and requires an explicit Responses
// terminal event. EOF alone cannot complete text or function arguments.
func (sc *StreamConverter) Finish() ([][]byte, *errclass.Error) {
	if sc.done {
		return nil, nil
	}
	sc.ending = true
	sc.framer.End()
	events, done, eErr := sc.Feed(nil)
	if eErr != nil {
		return nil, eErr
	}
	if !done {
		return nil, errclass.Translation("Responses stream ended before a terminal state")
	}
	return events, nil
}

// convertEvent routes one complete SSE frame to a conversion target:
// handled event types decode into their typed structs and share the
// field extraction below; unknown types only validate their payload.
func (sc *StreamConverter) convertEvent(eventType, payload string) ([][]byte, bool, *errclass.Error) {
	switch eventType {
	case "response.created":
		var ev createdEvent
		if eErr := decodeEvent(eventType, payload, &ev); eErr != nil {
			return nil, false, eErr
		}
		if eErr := sc.checkTarget(); eErr != nil {
			return nil, false, eErr
		}
		sc.captureResponse(&ev.Response)
		if sc.source == "openai" {
			return [][]byte{sc.chatChunks().RoleChunk()}, false, nil
		}
		// Input token count only becomes known at completion; Claude
		// clients read authoritative usage from message_delta, so
		// starting at zero is lossless here.
		return [][]byte{sc.claudeChunks().MessageStart(0)}, false, nil
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.done", "response.reasoning_text.done":
		var ev reasoningEvent
		if eErr := decodeEvent(eventType, payload, &ev); eErr != nil {
			return nil, false, eErr
		}
		if eErr := sc.checkTarget(); eErr != nil {
			return nil, false, eErr
		}
		kind, index := "content", ev.ContentIndex
		if strings.Contains(eventType, "summary") {
			kind, index = "summary", ev.SummaryIndex
		}
		key := fmt.Sprintf("%s/%s/%d", ev.ItemID, kind, index)
		if strings.HasSuffix(eventType, ".done") {
			events := sc.reasoningPart(key, ev.Text, true)
			if sc.source == "claude" {
				events = append(events, sc.queueMessagesContent("reasoning_stop", "")...)
			}
			return events, false, nil
		}
		return sc.reasoningPart(key, ev.Delta, false), false, nil
	case "response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done":
		var ev textDeltaEvent
		if eErr := decodeEvent(eventType, payload, &ev); eErr != nil {
			return nil, false, eErr
		}
		if eErr := sc.checkTarget(); eErr != nil {
			return nil, false, eErr
		}
		refused := strings.HasPrefix(eventType, "response.refusal.")
		complete := strings.HasSuffix(eventType, ".done")
		text := ev.Delta
		if complete {
			text = ev.Text
			if refused {
				text = ev.Refusal
			}
		}
		return sc.visiblePart(ev.ItemID, ev.ContentIndex, refused, text, complete)
	case "response.output_item.added", "response.output_item.done":
		var ev itemEvent
		if eErr := decodeEvent(eventType, payload, &ev); eErr != nil {
			return nil, false, eErr
		}
		if eErr := sc.checkTarget(); eErr != nil {
			return nil, false, eErr
		}
		if ev.Item.Type == "reasoning" {
			return sc.reasoningSnapshot(ev.Item), false, nil
		}
		if ev.Item.Type == "message" {
			return sc.messageSnapshot(ev.Item)
		}
		return sc.outputItem(&ev.Item, eventType == "response.output_item.done")
	case "response.function_call_arguments.done":
		var ev argsDeltaEvent
		if eErr := decodeEvent(eventType, payload, &ev); eErr != nil {
			return nil, false, eErr
		}
		if eErr := sc.checkTarget(); eErr != nil {
			return nil, false, eErr
		}
		item := functionCallItem{Type: "function_call", ID: ev.ItemID, CallID: ev.CallID, Name: ev.Name, Arguments: ev.Arguments}
		return sc.outputItem(&item, true)
	case "response.function_call_arguments.delta":
		var ev argsDeltaEvent
		if eErr := decodeEvent(eventType, payload, &ev); eErr != nil {
			return nil, false, eErr
		}
		if eErr := sc.checkTarget(); eErr != nil {
			return nil, false, eErr
		}
		return sc.argsFragment(ev.ItemID, ev.Delta)
	case "response.completed", "response.incomplete":
		var ev terminalEvent
		if eErr := decodeEvent(eventType, payload, &ev); eErr != nil {
			return nil, false, eErr
		}
		if eErr := sc.checkTarget(); eErr != nil {
			return nil, false, eErr
		}
		details := shared.UsageDetails{}
		if ev.Response.Usage.InputDetails != nil {
			if ev.Response.Usage.InputDetails.CachedTokens != nil {
				v := int64(*ev.Response.Usage.InputDetails.CachedTokens)
				details.CachedTokens = &v
			}
			if ev.Response.Usage.InputDetails.CacheWriteTokens != nil {
				v := int64(*ev.Response.Usage.InputDetails.CacheWriteTokens)
				details.CacheWriteTokens = &v
			}
		}
		if ev.Response.Usage.OutputDetails != nil && ev.Response.Usage.OutputDetails.ReasoningTokens != nil {
			v := int64(*ev.Response.Usage.OutputDetails.ReasoningTokens)
			details.ReasoningTokens = &v
		}
		var events [][]byte
		for _, item := range ev.Response.Output {
			switch item.Type {
			case "reasoning":
				events = append(events, sc.reasoningSnapshot(item)...)
			case "message":
				more, _, eErr := sc.messageSnapshot(item)
				if eErr != nil {
					return nil, false, eErr
				}
				events = append(events, more...)
			case "function_call":
				more, _, eErr := sc.outputItem(&item, true)
				if eErr != nil {
					return nil, false, eErr
				}
				events = append(events, more...)
			}
		}
		tail, done, eErr := sc.terminal(eventType == "response.incomplete", int(ev.Response.Usage.InputTokens), int(ev.Response.Usage.OutputTokens), details)
		return append(events, tail...), done, eErr
	case "response.failed", "error":
		var ev failureEvent
		if eErr := decodeEvent(eventType, payload, &ev); eErr != nil {
			return nil, false, eErr
		}
		if eErr := sc.checkTarget(); eErr != nil {
			return nil, false, eErr
		}
		return nil, false, failureError(&ev)
	default:
		// Informational events (response.in_progress, annotations)
		// have no client equivalent and are omitted
		// per the FR-005/FR-006 compatibility policy; the payload is
		// validated without materializing a discarded generic graph.
		if payload != "" && !json.Valid([]byte(payload)) {
			return nil, false, errclass.Translation(fmt.Sprintf(
				"malformed %s event payload: %s", eventType, shared.RedactedSnippet(payload)))
		}
		return nil, false, nil
	}
}

// passthroughEvent echoes native Responses frames verbatim (the framer's
// raw block, byte-identical to the upstream bytes); terminal events flip
// done after the common terminal validation. Failure/error payloads are parsed
// best-effort, so an unparseable failure degrades to a retryable error rather
// than failing the passthrough contract. Items for tools originally
// declared as custom restore to custom_tool_call shapes so native clients
// can dispatch them.
func (sc *StreamConverter) passthroughEvent(eventType, payload string, raw []byte) ([][]byte, bool, *errclass.Error) {
	switch eventType {
	case "response.completed", "response.incomplete":
		return [][]byte{raw}, true, nil
	case "response.failed", "error":
		var ev failureEvent
		json.Unmarshal([]byte(payload), &ev)
		return nil, false, failureError(&ev)
	case "response.output_item.added", "response.output_item.done":
		if sc.respTools != nil {
			if rewritten, ok := sc.restoreCustomItem(eventType, payload); ok {
				return [][]byte{rewritten}, false, nil
			}
		}
		return [][]byte{raw}, false, nil
	case "response.function_call_arguments.done":
		if sc.respTools != nil {
			if rewritten, ok := sc.restoreCustomInputDone(payload); ok {
				return [][]byte{rewritten}, false, nil
			}
		}
		return [][]byte{raw}, false, nil
	default:
		return [][]byte{raw}, false, nil
	}
}

// restoreCustomItem rewrites a function_call output item for a custom tool
// into its custom_tool_call shape; non-custom items report false so the
// caller falls back to verbatim passthrough.
func (sc *StreamConverter) restoreCustomItem(eventType, payload string) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return nil, false
	}
	item, ok := doc["item"].(map[string]any)
	if !ok {
		return nil, false
	}
	typ, _ := item["type"].(string)
	if typ != "function_call" {
		return nil, false
	}
	name, _ := item["name"].(string)
	if !sc.respTools.IsCustom(name) {
		return nil, false
	}
	args, _ := item["arguments"].(string)
	item["type"] = "custom_tool_call"
	if eventType == "response.output_item.added" {
		item["input"] = ""
	} else {
		item["input"] = shared.UnwrapCustomToolInput(args)
	}
	delete(item, "arguments")
	// Remember both item identifiers: a later
	// response.function_call_arguments.done may reference the item by
	// item_id alone without repeating the tool name.
	if id, _ := item["id"].(string); id != "" {
		sc.customItems[id] = struct{}{}
	}
	if id, _ := item["call_id"].(string); id != "" {
		sc.customItems[id] = struct{}{}
	}
	return shared.SSEEvent(eventType, doc), true
}

// restoreCustomInputDone rewrites a function_call_arguments.done event for
// a custom tool into its custom_tool_call_input.done shape; non-custom
// events report false so the caller falls back to verbatim passthrough.
func (sc *StreamConverter) restoreCustomInputDone(payload string) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return nil, false
	}
	custom := false
	if name, _ := doc["name"].(string); sc.respTools.IsCustom(name) {
		custom = true
	}
	if !custom {
		if id, _ := doc["item_id"].(string); id != "" {
			_, custom = sc.customItems[id]
		}
	}
	if !custom {
		return nil, false
	}
	args, _ := doc["arguments"].(string)
	out := map[string]any{
		"type":  "response.custom_tool_call_input.done",
		"input": shared.UnwrapCustomToolInput(args),
	}
	if v, ok := doc["item_id"]; ok {
		out["item_id"] = v
	}
	if v, ok := doc["output_index"]; ok {
		out["output_index"] = v
	}
	return shared.SSEEvent("response.custom_tool_call_input.done", out), true
}

// textDelta emits one output text delta: a plain Chat Completions content
// chunk, or a Messages content_block_delta that auto-opens (and may
// reopen) the text block.
func (sc *StreamConverter) emitTextDelta(delta string) ([][]byte, bool, *errclass.Error) {
	if sc.source == "openai" {
		return [][]byte{sc.chatChunks().Delta(map[string]any{"content": delta})}, false, nil
	}
	var out [][]byte
	if !sc.textOpen {
		// A tool_use block start closed the previous text block; a
		// later text delta (legal Responses interleaving) reopens a
		// fresh text block at the next free index to keep the
		// mandated start/delta/stop pairing per block index (same
		// stop/reopen pattern as the Chat Completions route). Any
		// still-open block stops first: close-before-next-start.
		out = append(out, sc.closeOpenBlocks()...)
		sc.textIndex = sc.allocIndex()
		sc.textOpen = true
		sc.openBlocks = append(sc.openBlocks, sc.textIndex)
		out = append(out, sc.claudeChunks().ContentBlockStart(sc.textIndex, "text", map[string]any{"text": ""}))
	}
	out = append(out, sc.claudeChunks().ContentBlockDelta(sc.textIndex,
		map[string]any{"type": "text_delta", "text": delta}))
	return out, false, nil
}

// outputItem handles output_item.added/.done through the shared tracker:
// Chat Completions announces/replays tool_calls entries, Messages opens
// tool_use blocks with mandated pairing; non-function items are ignored.
func (sc *StreamConverter) outputItem(item *functionCallItem, complete bool) ([][]byte, bool, *errclass.Error) {
	if item.Type != "function_call" {
		return nil, false, nil
	}
	if item.ID == "" && item.CallID == "" {
		return nil, false, errclass.Translation("Responses tool item has no item identity")
	}
	st := sc.tracker.Observe(item.CallID, item.ID, item.Name, item.Arguments, complete)
	return sc.emitToolState(st)
}

// Argument deltas may precede tool identity. Both target protocols buffer
// those bytes until the real call_id and name are available.
func (sc *StreamConverter) argsFragment(itemID, delta string) ([][]byte, bool, *errclass.Error) {
	if itemID == "" {
		return nil, false, errclass.Translation("Responses tool arguments delta has no item identity")
	}
	return sc.emitToolState(sc.tracker.StreamArgs(itemID, delta))
}

func (sc *StreamConverter) emitToolState(st *toolCallState) ([][]byte, bool, *errclass.Error) {
	sc.toolCallsSeen = true
	args := st.args.String()
	if sc.source == "openai" {
		if !st.hasIdentity() {
			return nil, false, nil
		}
		if !st.announced {
			st.announced = true
			st.sent = len(args)
			return [][]byte{sc.chatChunks().Delta(map[string]any{"tool_calls": []any{shared.CCToolCallOpeningEntry(st.index, st.callID, st.name, args)}})}, false, nil
		}
		tail := args[st.sent:]
		if tail == "" {
			return nil, false, nil
		}
		st.sent = len(args)
		return [][]byte{sc.chatChunks().Delta(map[string]any{"tool_calls": []any{map[string]any{"index": st.index, "function": map[string]any{"arguments": tail}}}})}, false, nil
	}
	events, eErr := sc.queueMessagesTool(st.index, st.callID, st.name, args[st.sent:], st.complete)
	if eErr == nil {
		st.sent = len(args)
	}
	return events, false, eErr
}

// terminal renders the completed/incomplete tail: Chat Completions gets a
// finish_reason chunk plus [DONE]; Messages gets close-before-stop block
// pairing, message_delta (stop_sequence omitted entirely) and
// message_stop. Shared precedence: tool calls outrank the status-derived
// reason, so response.incomplete cannot downgrade them.
func (sc *StreamConverter) terminal(incomplete bool, in, out int, details shared.UsageDetails) ([][]byte, bool, *errclass.Error) {
	for _, tool := range sc.tracker.states {
		if !tool.hasIdentity() {
			return nil, false, errclass.Translation("Responses stream ended before tool identity was complete")
		}
	}
	st := "completed"
	if incomplete {
		st = "incomplete"
	}
	if sc.source == "openai" {
		finish := shared.TerminalReason(sc.toolCallsSeen, "tool_calls", shared.FinishWithRefusal(shared.CCFinishFromResponseStatus(st), sc.refused))
		// Always attached (F-R6 parity with the Messages route's terminal
		// chunk): typed clients prefer a stable schema, zero-valued fields
		// when upstream reported none. Shared kernel keeps total_tokens
		// consistent with the non-stream Chat Completions mapper.
		chunk := sc.chatChunks().Finish(finish, shared.CCUsageFrom(int64(in), int64(out), details))
		return [][]byte{chunk}, true, nil
	}
	statusStop := shared.FinishToClaudeStop(shared.FinishWithRefusal(shared.CCFinishFromResponseStatus(st), sc.refused))
	stop := shared.TerminalReason(sc.toolCallsSeen, "tool_use", statusStop)
	cacheRead, cacheWrite := details.CachedTokens, details.CacheWriteTokens
	em := sc.claudeChunks()
	for _, tool := range sc.messagesTools {
		tool.complete = true
	}
	events := sc.drainMessages()
	for _, idx := range sc.openBlocks {
		events = append(events, em.ContentBlockStop(idx))
	}
	sc.openBlocks = nil
	events = append(events,
		em.MessageDelta(&stop, shared.ClaudeUsage(shared.ClampSubtract(int64(in), cacheRead, cacheWrite), int64(out), cacheRead, cacheWrite)),
		em.MessageStop(),
	)
	return events, true, nil
}

// chatChunks binds the shared chunk kernel to the captured upstream
// response identity so this route's frames cannot diverge from the
// sibling Messages-route synthesizer (FR-006); a zero Created defaults
// to render-time now inside the kernel.
func (sc *StreamConverter) chatChunks() shared.ChatChunkBuilder {
	return shared.ChatChunkBuilder{ID: sc.id, Model: sc.model, Created: sc.created}
}

// claudeChunks binds the shared Claude emitter kernel to the captured
// upstream response identity so this route's Messages frames cannot
// diverge from the sibling Chat Completions-route synthesizer (FR-006).
func (sc *StreamConverter) claudeChunks() shared.ClaudeEventEmitter {
	return shared.NewClaudeEventEmitter(sc.id, sc.model)
}

// closeOpenBlocks emits content_block_stop for every currently open
// content block and resets open-block tracking, so the next
// content_block_start obeys Anthropic's close-before-next-start rule;
// the text block is marked closed so a later text delta reopens a fresh
// block (stop/reopen parity with the Chat Completions route).
func (sc *StreamConverter) closeOpenBlocks() [][]byte {
	if len(sc.openBlocks) == 0 {
		return nil
	}
	em := sc.claudeChunks()
	var out [][]byte
	for _, idx := range sc.openBlocks {
		out = append(out, em.ContentBlockStop(idx))
	}
	sc.openBlocks = nil
	sc.textOpen = false
	sc.reasonOpen = false
	return out
}

// argsDelta renders one input_json_delta content block delta.
func (sc *StreamConverter) argsDelta(index int, partial string) []byte {
	return sc.claudeChunks().ContentBlockDelta(index,
		map[string]any{"type": "input_json_delta", "partial_json": partial})
}

// captureResponse records stream identity from response.created.
func (sc *StreamConverter) captureResponse(resp *responseMeta) {
	if resp.ID != "" {
		sc.id = resp.ID
	}
	if resp.Model != "" {
		sc.model = resp.Model
	}
	sc.created = int64(resp.CreatedAt)
}

// failureError maps response.failed / error events onto FR-009 classes,
// honoring an upstream status_code when present (§7 semantics); without
// one the failure is treated as a retryable upstream server failure.
func failureError(ev *failureEvent) *errclass.Error {
	status := int(ev.StatusCode)
	if status == 0 {
		status = int(ev.Response.StatusCode)
	}
	msg := ev.Message
	if msg == "" {
		msg = ev.Response.Error.Message
	}
	if status > 0 {
		return errclass.FromStatus(status, msg)
	}
	return errclass.UpstreamFallback(msg)
}

type reasoningEvent struct {
	ItemID       string `json:"item_id"`
	SummaryIndex int    `json:"summary_index"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta"`
	Text         string `json:"text"`
}

// Complete part/item/terminal snapshots fill missing tails without replaying
// text already delivered as deltas. Summary and content use separate keys.
func (sc *StreamConverter) reasoningSnapshot(item functionCallItem) [][]byte {
	var events [][]byte
	for i, p := range item.Summary {
		if p.Type == "summary_text" {
			events = append(events, sc.reasoningPart(fmt.Sprintf("%s/summary/%d", item.ID, i), p.Text, true)...)
		}
	}
	for i, p := range item.Content {
		if p.Type == "reasoning_text" || p.Type == "text" {
			events = append(events, sc.reasoningPart(fmt.Sprintf("%s/content/%d", item.ID, i), p.Text, true)...)
		}
	}
	return events
}

func (sc *StreamConverter) reasoningPart(key, text string, complete bool) [][]byte {
	if sc.reasoningParts == nil {
		sc.reasoningParts = map[string]*strings.Builder{}
	}
	part := sc.reasoningParts[key]
	if part == nil {
		part = &strings.Builder{}
		sc.reasoningParts[key] = part
	}
	if complete {
		previous := part.String()
		if !strings.HasPrefix(text, previous) {
			return nil
		}
		text = text[len(previous):]
	}
	if text == "" {
		return nil
	}
	part.WriteString(text)
	if sc.source == "openai" {
		return [][]byte{sc.chatChunks().Delta(map[string]any{"reasoning_content": text})}
	}
	return sc.queueMessagesContent("reasoning", text)
}

// Text/refusal done and item/terminal snapshots can carry the full content
// without deltas. Separate per-part accumulation prevents duplicate output.
func (sc *StreamConverter) visiblePart(id string, index int, refused bool, text string, complete bool) ([][]byte, bool, *errclass.Error) {
	if sc.visibleParts == nil {
		sc.visibleParts = map[string]*strings.Builder{}
	}
	key := fmt.Sprintf("%s/%d/%t", id, index, refused)
	part := sc.visibleParts[key]
	if part == nil {
		part = &strings.Builder{}
		sc.visibleParts[key] = part
	}
	if complete {
		previous := part.String()
		if !strings.HasPrefix(text, previous) {
			return nil, false, nil
		}
		text = text[len(previous):]
	}
	if text == "" {
		return nil, false, nil
	}
	part.WriteString(text)
	if refused {
		sc.refused = true
		if sc.source == "openai" {
			return [][]byte{sc.chatChunks().Delta(map[string]any{"refusal": text})}, false, nil
		}
	}
	return sc.textDelta(text)
}

func (sc *StreamConverter) messageSnapshot(item functionCallItem) ([][]byte, bool, *errclass.Error) {
	var events [][]byte
	for i, part := range item.Content {
		text := part.Text
		if part.Type == "refusal" {
			text = part.Refusal
		} else if part.Type != "output_text" {
			continue
		}
		more, _, eErr := sc.visiblePart(item.ID, i, part.Type == "refusal", text, true)
		if eErr != nil {
			return nil, false, eErr
		}
		events = append(events, more...)
	}
	return events, false, nil
}
