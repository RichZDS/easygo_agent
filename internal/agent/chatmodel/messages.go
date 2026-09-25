package chatmodel

import (
	"encoding/json"
	"fmt"

	"easygo-agent/pkg/ai"
	"github.com/cloudwego/eino/schema"
)

// MessagesToAI projects the existing persistence seam into the canonical wire
// protocol. Unsupported content is rejected rather than silently omitted.
func MessagesToAI(messages []*schema.AgenticMessage) ([]ai.Message, error) {
	out := make([]ai.Message, 0, len(messages))
	for _, m := range messages {
		if m == nil {
			return nil, fmt.Errorf("nil message")
		}
		msg := ai.Message{Role: string(m.Role)}
		var providerStates []string
		if raw, ok := m.Extra["ai_provider_states"]; ok {
			encoded, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("invalid opaque provider state storage")
			}
			if err := json.Unmarshal([]byte(encoded), &providerStates); err != nil {
				return nil, err
			}
			if len(providerStates) != len(m.ContentBlocks) {
				return nil, fmt.Errorf("opaque provider states do not match content blocks")
			}
		}
		for index, b := range m.ContentBlocks {
			if b == nil {
				return nil, fmt.Errorf("nil content block")
			}
			var block ai.Block
			switch {
			case b.UserInputText != nil:
				block = ai.Block{Type: "text", Text: b.UserInputText.Text}
			case b.AssistantGenText != nil:
				block = ai.Block{Type: "text", Text: b.AssistantGenText.Text}
			case b.Reasoning != nil:
				if b.Reasoning.Signature != "" || b.Reasoning.OpenAIExtension != nil {
					return nil, fmt.Errorf("signed/encrypted reasoning is not supported by canonical protocol")
				}
				block = ai.Block{Type: "reasoning", Text: b.Reasoning.Text}
			case b.FunctionToolCall != nil:
				c := b.FunctionToolCall
				block = ai.Block{Type: "tool_call", ID: c.CallID, Name: c.Name, Arguments: json.RawMessage(c.Arguments)}
			case b.FunctionToolResult != nil:
				r := b.FunctionToolResult
				block = ai.Block{Type: "tool_result", ID: r.CallID, Name: r.Name}
				for i, c := range r.Content {
					if c == nil || c.Text == nil {
						return nil, fmt.Errorf("non-text tool result is unsupported")
					}
					if i > 0 {
						block.Text += "\n"
					}
					block.Text += c.Text.Text
				}
				if m.Extra != nil {
					block.IsError, _ = m.Extra["tool_result_is_error"].(bool)
				}
			case b.UserInputImage != nil:
				i := b.UserInputImage
				block = ai.Block{Type: "image", URL: i.URL, Data: i.Base64Data, MediaType: i.MIMEType}
			default:
				return nil, fmt.Errorf("unsupported message block %q", b.Type)
			}
			if len(providerStates) > 0 {
				block.ProviderState = json.RawMessage(providerStates[index])
			}
			// Eino stores tool results as user messages. Canonical protocol uses tool.
			role := string(m.Role)
			if block.Type == "tool_result" {
				role = "tool"
			}
			if len(msg.Content) > 0 && msg.Role != role {
				out = append(out, msg)
				msg = ai.Message{Role: role}
			}
			msg.Role = role
			msg.Content = append(msg.Content, block)
		}
		out = append(out, msg)
	}
	return out, nil
}

func MessageFromAI(m ai.Message) *schema.AgenticMessage {
	out := &schema.AgenticMessage{Role: schema.AgenticRoleType(m.Role)}
	if m.Role == "tool" {
		out.Role = schema.AgenticRoleTypeUser
	}
	for _, b := range m.Content {
		var c *schema.ContentBlock
		switch b.Type {
		case "text":
			if m.Role == "assistant" {
				c = schema.NewContentBlock(&schema.AssistantGenText{Text: b.Text})
			} else {
				c = schema.NewContentBlock(&schema.UserInputText{Text: b.Text})
			}
		case "reasoning":
			c = schema.NewContentBlock(&schema.Reasoning{Text: b.Text})
		case "tool_call":
			c = schema.NewContentBlock(&schema.FunctionToolCall{CallID: b.ID, Name: b.Name, Arguments: string(b.Arguments)})
		case "tool_result":
			c = schema.NewContentBlock(&schema.FunctionToolResult{CallID: b.ID, Name: b.Name, Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: b.Text}}}})
			out.Extra = map[string]any{"tool_result_is_error": b.IsError}
		case "image":
			c = schema.NewContentBlock(&schema.UserInputImage{URL: b.URL, Base64Data: b.Data, MIMEType: b.MediaType})
		}
		if c != nil {
			out.ContentBlocks = append(out.ContentBlocks, c)
		}
	}
	var states []string
	hasState := false
	for _, b := range m.Content {
		states = append(states, string(b.ProviderState))
		hasState = hasState || len(b.ProviderState) > 0
	}
	if hasState {
		encoded, _ := json.Marshal(states)
		if out.Extra == nil {
			out.Extra = map[string]any{}
		}
		out.Extra["ai_provider_states"] = string(encoded)
	}
	return out
}

func ResponseFromAI(r ai.Response) *schema.AgenticMessage {
	m := MessageFromAI(r.Message)
	m.ResponseMeta = &schema.AgenticResponseMeta{Extension: map[string]any{"id": r.ID, "model": r.Model, "finish_reason": r.FinishReason, "usage": r.Usage, "cost": r.Cost}}
	if r.Usage.Known {
		m.ResponseMeta.TokenUsage = &schema.TokenUsage{PromptTokens: int(r.Usage.InputTokens), CompletionTokens: int(r.Usage.OutputTokens), TotalTokens: int(r.Usage.InputTokens + r.Usage.OutputTokens), PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: int(r.Usage.CacheReadTokens)}}
	}
	return m
}

func DeltaFromAI(e ai.Event) (*schema.AgenticMessage, error) {
	m := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
	meta := &schema.StreamingMeta{Index: e.Index}
	var b *schema.ContentBlock
	switch e.Type {
	case "text_delta":
		b = schema.NewContentBlockChunk(&schema.AssistantGenText{Text: e.Delta}, meta)
	case "reasoning_delta":
		b = schema.NewContentBlockChunk(&schema.Reasoning{Text: e.Delta}, meta)
	case "tool_call_delta":
		b = schema.NewContentBlockChunk(&schema.FunctionToolCall{CallID: e.ID, Name: e.Name, Arguments: e.Delta}, meta)
	default:
		return nil, fmt.Errorf("unsupported model event %q", e.Type)
	}
	m.ContentBlocks = []*schema.ContentBlock{b}
	return m, nil
}

func ToolsToAI(tools []*schema.ToolInfo) ([]ai.Tool, error) {
	result := make([]ai.Tool, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("nil tool definition")
		}
		parameters := json.RawMessage(`{"type":"object","properties":{}}`)
		if t.ParamsOneOf != nil {
			s, err := t.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, err
			}
			parameters, err = json.Marshal(s)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, ai.Tool{Name: t.Name, Description: t.Desc, Parameters: parameters})
	}
	return result, nil
}
