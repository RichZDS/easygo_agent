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

// StreamEvent 是 ExecutionService 向 SSE 层推送的统一事件信封。
type StreamEvent struct {
	Type    string // 事件类型：turn、message、stream_chunk、error 等
	Payload any    // 具体载荷，由 controller 序列化为 SSE data
}

// EmitFunc 向客户端推送流式事件的回调；返回错误视为客户端断连。
type EmitFunc func(StreamEvent) error

var errClientDisconnected = errors.New("client stream disconnected")

// EinoSSEEvent 遵循 Eino ADK 官方 HTTP-SSE 示例的载荷格式。
// EasyGo 在 controller 层额外包裹 Turn 信封，不修改此结构。
type EinoSSEEvent struct {
	Type       string            `json:"type"`
	AgentName  string            `json:"agent_name,omitempty"`
	RunPath    string            `json:"run_path,omitempty"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []schema.ToolCall `json:"tool_calls,omitempty"`
	ActionType string            `json:"action_type,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// TurnSSEEvent 是 EasyGo 自有的轮次状态事件，与 EinoSSEEvent 并列推送。
type TurnSSEEvent struct {
	TurnID string `json:"turn_id"`
	Status string `json:"status"` // running / completed / cancelled / failed
	Error  string `json:"error,omitempty"`
}

// ExecutionService 驱动已准备好的 Turn 执行 Eino Runner，并将事件流式推送给客户端。
type ExecutionService struct {
	turns *TurnService
}

// NewExecutionService 创建执行服务，依赖 TurnService 做终态持久化。
func NewExecutionService(turns *TurnService) *ExecutionService {
	return &ExecutionService{turns: turns}
}

// Run 执行一轮对话：推送 running 事件、消费 Runner 迭代器、最终调用 Finalize 落库。
// 返回值 status 为 Turn 终态，error 为执行或持久化过程中的错误（幂等重试场景可能非 nil）。
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
	// 与 Prepare 写入的 expires_at 对齐，超时后 context 自动取消 Runner。
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
			// WillRetryError 表示 Eino 内部即将重试，不算终态失败。
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
		// 当前未接入 CheckPointStore，不支持 Agent 中断恢复。
		runErr = fmt.Errorf("agent interruption is not supported without a CheckPointStore")
	}
	return s.finalizeAfterStream(runCtx, prepared, outputs, runErr, emit)
}

// finalizeAfterStream 根据运行结果确定 Turn 终态，落库后推送 terminal turn 事件。
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

	// 持久化使用独立 context，避免客户端断连导致 Finalize 被取消。
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

// consumeMessageOutput 处理 Runner 输出的单条消息或流式分片，逐块推送 SSE 并合并为完整消息。
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

// emitAction 将 Agent 动作事件（转交、中断、退出）推送给客户端。
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

// einoErrorEvent 将 AgentEvent 错误包装为 SSE 错误载荷。
func einoErrorEvent(event *adk.AgentEvent) EinoSSEEvent {
	return EinoSSEEvent{
		Type:      "error",
		AgentName: event.AgentName,
		RunPath:   fmt.Sprintf("%v", event.RunPath),
		Error:     event.Err.Error(),
	}
}

// emitExecutionEvent 调用 emit 并在失败时标记为客户端断连。
func emitExecutionEvent(emit EmitFunc, event StreamEvent) error {
	if err := emit(event); err != nil {
		return fmt.Errorf("%w: %v", errClientDisconnected, err)
	}
	return nil
}

// statusName 将 Turn 状态码转为 SSE 字符串。
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
