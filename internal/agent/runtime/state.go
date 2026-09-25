package agentruntime

import (
	"context"
	"sync"

	"easygo-agent/internal/conversation"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
)

type captureKey struct{}
type stateCapture struct {
	mu       sync.Mutex
	messages []*schema.AgenticMessage
}

func (s *stateCapture) get() []*schema.AgenticMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messages
}

// StateMiddleware captures Eino's actual final state, including any context
// rewrites by summarization. Never reconstruct history from displayed text.
type StateMiddleware struct {
	adk.TypedBaseChatModelAgentMiddleware[*schema.AgenticMessage]
}

func (m *StateMiddleware) AfterAgent(ctx context.Context, state *adk.TypedChatModelAgentState[*schema.AgenticMessage]) (context.Context, error) {
	return ctx, CaptureState(ctx, state.Messages)
}

// CaptureState records the native loop's actual context at a synchronous
// barrier. Durable turn publication remains owned by the existing runtime.
func CaptureState(ctx context.Context, state []*schema.AgenticMessage) error {
	capture, _ := ctx.Value(captureKey{}).(*stateCapture)
	if capture == nil {
		return nil
	}
	messages, err := conversation.Clone(state)
	if err != nil {
		return err
	}
	capture.mu.Lock()
	capture.messages = messages
	capture.mu.Unlock()
	return nil
}

func compressionEvent(event *adk.TypedAgentEvent[*schema.AgenticMessage]) (Event, bool) {
	if event.Action == nil {
		return Event{}, false
	}
	action, ok := event.Action.CustomizedAction.(*summarization.TypedCustomizedAction[*schema.AgenticMessage])
	if !ok {
		return Event{}, false
	}
	switch action.Type {
	case summarization.ActionTypeBeforeSummarize:
		return Event{Kind: EventCompressing}, true
	case summarization.ActionTypeAfterSummarize:
		return Event{Kind: EventCompressed}, true
	}
	return Event{}, false
}
