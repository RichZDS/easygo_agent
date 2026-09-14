package agentruntime

import (
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// EventProjector is the in-process test surface that turns Eino messages and
// compression actions into Event values. agentRun pumps the iterator; this
// module owns chunk/tool-draft/compression projection.
type EventProjector struct {
	pending       []Event
	text          strings.Builder
	startedTools  map[string]struct{}
	finishedTools map[string]struct{}
	toolDraft     toolCallDraft
}

type toolCallDraft struct {
	name      string
	callID    string
	arguments strings.Builder
}

func newEventProjector() *EventProjector {
	return &EventProjector{
		startedTools:  make(map[string]struct{}),
		finishedTools: make(map[string]struct{}),
	}
}

func (p *EventProjector) Text() string { return p.text.String() }

func (p *EventProjector) ProjectCompression(event *adk.TypedAgentEvent[*schema.AgenticMessage]) (Event, bool) {
	return compressionEvent(event)
}

func (p *EventProjector) ProjectMessage(message *schema.AgenticMessage) {
	if message == nil {
		return
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.Reasoning != nil && block.Reasoning.Text != "" {
			p.pending = append(p.pending, Event{Kind: EventReasoningDelta, Text: block.Reasoning.Text})
		}
		if block.AssistantGenText != nil && block.AssistantGenText.Text != "" {
			text := block.AssistantGenText.Text
			p.text.WriteString(text)
			p.pending = append(p.pending, Event{Kind: EventTextDelta, Text: text})
		}
		if block.FunctionToolCall != nil {
			p.projectToolCall(block.FunctionToolCall)
		}
		if block.FunctionToolResult != nil {
			p.flushToolDraft()
			p.projectToolResult(block.FunctionToolResult)
		}
	}
}

func (p *EventProjector) projectToolCall(call *schema.FunctionToolCall) {
	if call == nil {
		return
	}
	if call.CallID != "" && p.toolDraft.callID != "" && call.CallID != p.toolDraft.callID {
		p.flushToolDraft()
	}
	if call.CallID != "" {
		p.toolDraft.callID = call.CallID
	}
	if call.Name != "" {
		p.toolDraft.name = call.Name
	}
	if call.Arguments != "" {
		p.toolDraft.arguments.WriteString(call.Arguments)
	}
}

func (p *EventProjector) flushToolDraft() {
	if p.toolDraft.name == "" && p.toolDraft.callID == "" && p.toolDraft.arguments.Len() == 0 {
		return
	}
	name := p.toolDraft.name
	callID := p.toolDraft.callID
	arguments := p.toolDraft.arguments.String()
	p.toolDraft = toolCallDraft{}
	p.emitToolEvent(EventToolStarted, name, callID, arguments, "")
}

func (p *EventProjector) projectToolResult(result *schema.FunctionToolResult) {
	if result == nil {
		return
	}
	p.emitToolEvent(EventToolFinished, result.Name, result.CallID, "", toolResultText(result))
}

func (p *EventProjector) emitToolEvent(kind EventKind, name, callID, arguments, result string) {
	if callID == "" && name == "" {
		return
	}
	key := callID
	if key == "" {
		key = fmt.Sprintf("%s#%d", name, len(p.startedTools)+len(p.finishedTools)+1)
	}
	seen := p.startedTools
	if kind == EventToolFinished {
		seen = p.finishedTools
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	if name == "" {
		name = key
	}
	p.pending = append(p.pending, Event{
		Kind:      kind,
		Tool:      name,
		CallID:    callID,
		Arguments: arguments,
		Result:    result,
	})
}

func (p *EventProjector) popPending() (Event, bool) {
	if len(p.pending) == 0 {
		return Event{}, false
	}
	event := p.pending[0]
	p.pending[0] = Event{}
	p.pending = p.pending[1:]
	return event, true
}

func toolResultText(result *schema.FunctionToolResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if block == nil {
			continue
		}
		if block.Text != nil && block.Text.Text != "" {
			parts = append(parts, block.Text.Text)
			continue
		}
		if text := strings.TrimSpace(block.String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
