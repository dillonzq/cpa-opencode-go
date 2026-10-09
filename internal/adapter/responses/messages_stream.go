package responses

import (
	"strings"

	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
)

// Messages requires sequential content blocks. A tool remains at the head
// of the queue until its arguments are complete; later content waits rather
// than closing a block that may still receive argument fragments.
type messagesEmission struct {
	kind string
	text string
	tool *messagesTool
}

type messagesTool struct {
	id       string
	name     string
	args     strings.Builder
	index    int
	sent     int
	opened   bool
	closed   bool
	complete bool
}

func (sc *StreamConverter) queueMessagesTool(key int, id, name, delta string, complete bool) ([][]byte, *errclass.Error) {
	if sc.messagesTools == nil {
		sc.messagesTools = map[int]*messagesTool{}
	}
	tool := sc.messagesTools[key]
	if tool == nil {
		tool = &messagesTool{id: id, name: name, index: -1}
		sc.messagesTools[key] = tool
		sc.messagesQueue = append(sc.messagesQueue, messagesEmission{kind: "tool", tool: tool})
	}
	if !tool.opened && name != "" {
		// A queued tool can acquire its real identity before its block opens.
		tool.id, tool.name = id, name
	}
	if tool.closed && delta != "" {
		return nil, errclass.Translation("Responses tool arguments changed after tool completion")
	}
	tool.args.WriteString(delta)
	tool.complete = tool.complete || complete
	return sc.drainMessages(), nil
}

func (sc *StreamConverter) queueMessagesContent(kind, text string) [][]byte {
	sc.messagesQueue = append(sc.messagesQueue, messagesEmission{kind: kind, text: text})
	return sc.drainMessages()
}

func (sc *StreamConverter) drainMessages() [][]byte {
	var events [][]byte
	for len(sc.messagesQueue) > 0 {
		entry := sc.messagesQueue[0]
		if entry.kind == "tool" {
			tool := entry.tool
			if !tool.opened {
				events = append(events, sc.closeOpenBlocks()...)
				tool.index = sc.allocIndex()
				tool.opened = true
				sc.openBlocks = append(sc.openBlocks, tool.index)
				events = append(events, sc.claudeChunks().ContentBlockStart(tool.index, "tool_use", map[string]any{"id": tool.id, "name": tool.name, "input": map[string]any{}}))
			}
			args := tool.args.String()
			if tool.sent < len(args) {
				events = append(events, sc.argsDelta(tool.index, args[tool.sent:]))
				tool.sent = len(args)
			}
			if !tool.complete {
				break
			}
			events = append(events, sc.closeOpenBlocks()...)
			tool.closed = true
		} else {
			switch entry.kind {
			case "text":
				more, _, _ := sc.emitTextDelta(entry.text)
				events = append(events, more...)
			case "reasoning":
				events = append(events, sc.emitReasoningDelta(entry.text)...)
			case "reasoning_stop":
				if sc.reasonOpen {
					events = append(events, sc.closeOpenBlocks()...)
				}
			}
		}
		sc.messagesQueue[0] = messagesEmission{}
		sc.messagesQueue = sc.messagesQueue[1:]
	}
	return events
}

func (sc *StreamConverter) textDelta(delta string) ([][]byte, bool, *errclass.Error) {
	if sc.source == "claude" {
		return sc.queueMessagesContent("text", delta), false, nil
	}
	return sc.emitTextDelta(delta)
}

func (sc *StreamConverter) emitReasoningDelta(text string) [][]byte {
	var events [][]byte
	if !sc.reasonOpen {
		events = append(events, sc.closeOpenBlocks()...)
		sc.reasonIndex = sc.allocIndex()
		sc.reasonOpen = true
		sc.openBlocks = append(sc.openBlocks, sc.reasonIndex)
		events = append(events, sc.claudeChunks().ContentBlockStart(sc.reasonIndex, "thinking", map[string]any{"thinking": ""}))
	}
	return append(events, sc.claudeChunks().ContentBlockDelta(sc.reasonIndex, map[string]any{"type": "thinking_delta", "thinking": text}))
}
