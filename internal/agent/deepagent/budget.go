package deepagent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
)

const tokenFraming = 256

// Composition is the over-budget mix: dialogue vs heuristic skill vs tools.
type Composition struct {
	Total    int
	Dialogue int
	Skill    int
	Tools    int
}

// Measure splits counted input into dialogue, skill-catalog, and tool schema.
func Measure(messages []*schema.AgenticMessage, tools []*schema.ToolInfo) (Composition, error) {
	var out Composition
	for _, message := range messages {
		n, err := messageBytes(message)
		if err != nil {
			return Composition{}, err
		}
		if isSkillCatalogMessage(message) {
			out.Skill += n
			continue
		}
		out.Dialogue += n
	}
	for _, tool := range tools {
		n, err := toolBytes(tool)
		if err != nil {
			return Composition{}, err
		}
		out.Tools += n
	}
	out.Total = tokenFraming + out.Dialogue + out.Skill + out.Tools
	return out, nil
}

func messageBytes(message *schema.AgenticMessage) (int, error) {
	if message == nil {
		return 0, nil
	}
	data, err := json.Marshal(message)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func toolBytes(tool *schema.ToolInfo) (int, error) {
	if tool == nil {
		return 0, nil
	}
	data, err := json.Marshal(tool)
	if err != nil {
		return 0, err
	}
	n := len(data)
	if tool.ParamsOneOf == nil {
		return n, nil
	}
	parameters, err := tool.ParamsOneOf.ToJSONSchema()
	if err != nil {
		return 0, err
	}
	schemaJSON, err := json.Marshal(parameters)
	if err != nil {
		return 0, err
	}
	return n + len(schemaJSON), nil
}

func isSkillCatalogMessage(message *schema.AgenticMessage) bool {
	if message == nil || message.Role != schema.AgenticRoleTypeSystem {
		return false
	}
	text := messageText(message)
	return strings.Contains(text, "Skill Catalog") || strings.Contains(text, "## Directory") || strings.Contains(text, "load_skill")
}

func messageText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.UserInputText != nil {
			b.WriteString(block.UserInputText.Text)
		}
		if block.AssistantGenText != nil {
			b.WriteString(block.AssistantGenText.Text)
		}
		if block.FunctionToolCall != nil {
			b.WriteString(block.FunctionToolCall.Name)
			b.WriteString(block.FunctionToolCall.Arguments)
		}
	}
	return b.String()
}

func usedToolNames(messages []*schema.AgenticMessage) map[string]bool {
	out := map[string]bool{}
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, block := range message.ContentBlocks {
			if block != nil && block.FunctionToolCall != nil && block.FunctionToolCall.Name != "" {
				out[block.FunctionToolCall.Name] = true
			}
		}
	}
	return out
}

func lastUserText(messages []*schema.AgenticMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] != nil && messages[i].Role == schema.AgenticRoleTypeUser {
			return messageText(messages[i])
		}
	}
	return ""
}

func firstUserText(messages []*schema.AgenticMessage) string {
	for _, message := range messages {
		if message != nil && message.Role == schema.AgenticRoleTypeUser {
			return messageText(message)
		}
	}
	return ""
}

func schemaSystem(text string) *schema.AgenticMessage {
	return schema.SystemAgenticMessage(text)
}

func schemaUser(text string) *schema.AgenticMessage {
	return schema.UserAgenticMessage(text)
}

func schemaAssistant(text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: text})},
	}
}

func countedTotal(ctx context.Context, messages []*schema.AgenticMessage, tools []*schema.ToolInfo) (int, error) {
	return countInputTokens(ctx, &summarization.TypedTokenCounterInput[*schema.AgenticMessage]{
		Messages: messages,
		Tools:    tools,
	})
}
