// Package testutil contains deterministic models used only by tests.
package testutil

import (
	"context"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Model struct {
	GenerateFunc func(context.Context, []*schema.AgenticMessage) (*schema.AgenticMessage, error)
	StreamFunc   func(context.Context, []*schema.AgenticMessage) (*schema.StreamReader[*schema.AgenticMessage], error)
}

func (m *Model) Generate(ctx context.Context, in []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
	return m.GenerateFunc(ctx, in)
}
func (m *Model) Stream(ctx context.Context, in []*schema.AgenticMessage, _ ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	if m.StreamFunc != nil {
		return m.StreamFunc(ctx, in)
	}
	out, err := m.GenerateFunc(ctx, in)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{out}), nil
}
func Text(text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: text})}}
}
func ToolCall(id, args string) *schema.AgenticMessage {
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{Name: "calculator", CallID: id, Arguments: args})}}
}
