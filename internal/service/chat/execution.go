package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"easygo-agent/internal/model"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type StreamEvent struct {
	Type    string
	Payload any
}

type EmitFunc func(StreamEvent) error

var errClientDisconnected = errors.New("client stream disconnected")

// EinoSSEEvent follows the official Eino ADK HTTP-SSE example. EasyGo adds the
// Turn envelope at the controller boundary without rewriting this payload.
type EinoSSEEvent struct {
	Type       string            `json:"type"`
	AgentName  string            `json:"agent_name,omitempty"`
	RunPath    string            `json:"run_path,omitempty"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []schema.ToolCall `json:"tool_calls,omitempty"`
	ActionType string            `json:"action_type,omitempty"`
	Error      string            `json:"error,omitempty"`
}

type TurnSSEEvent struct {
	TurnID string `json:"turn_id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type ExecutionService struct {
	turns *TurnService
}

func NewExecutionService(turns *TurnService) *ExecutionService {
	return &ExecutionService{turns: turns}
}

func (s *ExecutionService) Run(
	requestCtx context.Context,
	prepared *PreparedTurn,
	emit EmitFunc,
) (uint8, error) {
	if prepared == nil || prepared.Turn == nil || prepared.Runner == nil {
		return model.TurnFailed, fmt.Errorf("prepared Eino turn is required")
	}
	if emit == nil {
		return model.TurnFailed, fmt.Errorf("stream emitter is required")
	}

	if err := emitExecutionEvent(emit, StreamEvent{
		Type: "turn",
		Payload: TurnSSEEvent{
			TurnID: prepared.Turn.TurnID,
			Status: "running",
		},
	}); err != nil {
		return s.finalizeAfterStream(requestCtx, prepared, nil, err, emit)
	}

	deadline := time.Now().Add(s.turns.runtime.Config().TurnTimeout)
	if prepared.Turn.ExpiresAt != nil {
		deadline = *prepared.Turn.ExpiresAt
	}
	runCtx, cancel := context.WithDeadline(requestCtx, deadline)
	defer cancel()

	iterator := prepared.Runner.Run(runCtx, prepared.Messages)
	outputs := make([]*schema.Message, 0, 1)
	var runErr error
	var interruption bool
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			if err := emitExecutionEvent(emit, StreamEvent{
				Type:    "error",
				Payload: einoErrorEvent(event),
			}); err != nil {
				runErr = err
				break
			}
			var willRetry *adk.WillRetryError
			if !errors.As(event.Err, &willRetry) {
				runErr = event.Err
			}
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			messages, err := consumeMessageOutput(runCtx, event, emit)
			outputs = append(outputs, messages...)
			if err != nil {
				runErr = err
				break
			}
		}
		if event.Action != nil {
			if err := emitAction(event, emit); err != nil {
				runErr = err
				break
			}
			if event.Action.Interrupted != nil {
				interruption = true
			}
		}
	}
	if interruption && runErr == nil {
		runErr = fmt.Errorf("agent interruption is not supported without a CheckPointStore")
	}
	return s.finalizeAfterStream(runCtx, prepared, outputs, runErr, emit)
}

func (s *ExecutionService) finalizeAfterStream(
	runCtx context.Context,
	prepared *PreparedTurn,
	outputs []*schema.Message,
	runErr error,
	emit EmitFunc,
) (uint8, error) {
	status := model.TurnCompleted
	errorCode := ""
	errorMessage := ""

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		status = model.TurnFailed
		errorCode = "execution_timeout"
		errorMessage = "Turn exceeded its execution deadline"
	case errors.Is(runCtx.Err(), context.Canceled):
		status = model.TurnCancelled
		errorCode = "cancelled"
		errorMessage = "Turn was cancelled by the client"
	case errors.Is(runErr, errClientDisconnected):
		status = model.TurnCancelled
		errorCode = "cancelled"
		errorMessage = "Turn was cancelled because the client stream disconnected"
	case runErr != nil:
		status = model.TurnFailed
		errorCode = "agent_execution"
		errorMessage = runErr.Error()
	}

	finalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.turns.Finalize(finalCtx, prepared, status, outputs, errorCode, errorMessage); err != nil {
		persistenceErr := fmt.Errorf("persist Turn terminal state: %w", err)
		_ = emit(StreamEvent{
			Type: "error",
			Payload: EinoSSEEvent{
				Type:  "error",
				Error: persistenceErr.Error(),
			},
		})
		_ = emit(StreamEvent{
			Type: "turn",
			Payload: TurnSSEEvent{
				TurnID: prepared.Turn.TurnID,
				Status: "failed",
				Error:  "persistence_error",
			},
		})
		return model.TurnFailed, persistenceErr
	}

	terminal := statusName(status)
	_ = emit(StreamEvent{
		Type: "turn",
		Payload: TurnSSEEvent{
			TurnID: prepared.Turn.TurnID,
			Status: terminal,
			Error:  errorMessage,
		},
	})
	return status, runErr
}

func consumeMessageOutput(
	ctx context.Context,
	event *adk.AgentEvent,
	emit EmitFunc,
) ([]*schema.Message, error) {
	output := event.Output.MessageOutput
	if output.Message != nil {
		message := output.Message
		eventType := "message"
		if message.Role == schema.Tool {
			eventType = "tool_result"
		}
		payload := EinoSSEEvent{
			Type:      eventType,
			AgentName: event.AgentName,
			RunPath:   fmt.Sprintf("%v", event.RunPath),
			Content:   message.Content,
			ToolCalls: message.ToolCalls,
		}
		if err := emitExecutionEvent(emit, StreamEvent{Type: eventType, Payload: payload}); err != nil {
			return []*schema.Message{message}, err
		}
		return []*schema.Message{message}, nil
	}
	if output.MessageStream == nil {
		return nil, nil
	}

	stream := output.MessageStream
	defer stream.Close()
	chunks := make([]*schema.Message, 0, 8)
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if len(chunks) == 0 {
				return nil, err
			}
			partial, concatErr := schema.ConcatMessages(chunks)
			if concatErr != nil {
				return nil, errors.Join(err, concatErr)
			}
			return []*schema.Message{partial}, err
		}
		if chunk == nil {
			continue
		}
		chunks = append(chunks, chunk)
		if chunk.Content != "" || chunk.ReasoningContent != "" ||
			len(chunk.AssistantGenMultiContent) > 0 {
			eventType := "stream_chunk"
			if chunk.Role == schema.Tool {
				eventType = "tool_result_chunk"
			}
			if err := emitExecutionEvent(emit, StreamEvent{
				Type: eventType,
				Payload: EinoSSEEvent{
					Type:      eventType,
					AgentName: event.AgentName,
					RunPath:   fmt.Sprintf("%v", event.RunPath),
					Content:   chunk.Content,
				},
			}); err != nil {
				partial, concatErr := schema.ConcatMessages(chunks)
				if concatErr != nil {
					return nil, errors.Join(err, concatErr)
				}
				return []*schema.Message{partial}, err
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, err
	}
	if len(message.ToolCalls) > 0 {
		if err := emitExecutionEvent(emit, StreamEvent{
			Type: "tool_calls",
			Payload: EinoSSEEvent{
				Type:      "tool_calls",
				AgentName: event.AgentName,
				RunPath:   fmt.Sprintf("%v", event.RunPath),
				ToolCalls: message.ToolCalls,
			},
		}); err != nil {
			return []*schema.Message{message}, err
		}
	}
	return []*schema.Message{message}, nil
}

func emitAction(event *adk.AgentEvent, emit EmitFunc) error {
	action := event.Action
	base := EinoSSEEvent{
		Type:      "action",
		AgentName: event.AgentName,
		RunPath:   fmt.Sprintf("%v", event.RunPath),
	}
	if action.TransferToAgent != nil {
		base.ActionType = "transfer"
		base.Content = fmt.Sprintf("Transfer to agent: %s", action.TransferToAgent.DestAgentName)
		if err := emitExecutionEvent(emit, StreamEvent{Type: "action", Payload: base}); err != nil {
			return err
		}
	}
	if action.Interrupted != nil {
		for _, interrupt := range action.Interrupted.InterruptContexts {
			payload := base
			payload.ActionType = "interrupted"
			payload.Content = fmt.Sprintf("%v", interrupt.Info)
			if stringer, ok := interrupt.Info.(fmt.Stringer); ok {
				payload.Content = stringer.String()
			}
			if err := emitExecutionEvent(emit, StreamEvent{Type: "action", Payload: payload}); err != nil {
				return err
			}
		}
	}
	if action.Exit {
		base.ActionType = "exit"
		base.Content = "Agent execution completed"
		return emitExecutionEvent(emit, StreamEvent{Type: "action", Payload: base})
	}
	return nil
}

func einoErrorEvent(event *adk.AgentEvent) EinoSSEEvent {
	return EinoSSEEvent{
		Type:      "error",
		AgentName: event.AgentName,
		RunPath:   fmt.Sprintf("%v", event.RunPath),
		Error:     event.Err.Error(),
	}
}

func emitExecutionEvent(emit EmitFunc, event StreamEvent) error {
	if err := emit(event); err != nil {
		return fmt.Errorf("%w: %v", errClientDisconnected, err)
	}
	return nil
}

func statusName(status uint8) string {
	switch status {
	case model.TurnCompleted:
		return "completed"
	case model.TurnCancelled:
		return "cancelled"
	default:
		return "failed"
	}
}
