package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"easygo-agent/internal/model"
	"easygo-agent/internal/platform/logger"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// StreamEvent 是 execution 层向 SSE 层推送的统一事件信封。
type StreamEvent struct {
	Type    string
	Payload any
}

// EmitFunc 将 StreamEvent 写入客户端 SSE 流；返回非 nil 表示客户端已断开。
type EmitFunc func(StreamEvent) error

var errClientDisconnected = errors.New("client stream disconnected")

// EinoSSEEvent 对应 Agent 执行过程中的细粒度 SSE 事件（message / stream_chunk / action / error）。
type EinoSSEEvent struct {
	Type       string `json:"type"`
	AgentName  string `json:"agent_name,omitempty"`
	RunPath    string `json:"run_path,omitempty"`
	Content    string `json:"content,omitempty"`
	ActionType string `json:"action_type,omitempty"`
	Error      string `json:"error,omitempty"`
}

// RunSSEEvent 对应 run 生命周期事件（running → succeeded / failed / cancelled）。
type RunSSEEvent struct {
	RunID  uint64 `json:"run_id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// ExecutionService 驱动 Eino TypedRunner，消费事件流并通过 emit 推送给客户端。
type ExecutionService struct {
	runs *RunService
}

func NewExecutionService(runs *RunService) *ExecutionService {
	return &ExecutionService{runs: runs}
}

// Run 执行已准备好的 Agent 运行：迭代 Eino 事件、推送 SSE、最后落库并发送终态事件。
func (s *ExecutionService) Run(
	requestCtx context.Context,
	prepared *PreparedRun, // 准备好的运行
	emit EmitFunc, // 发送事件到客户端
) (uint8, error) {
	if prepared == nil || prepared.Run == nil || prepared.Runner == nil {
		return model.AgentRunFailed, fmt.Errorf("prepared Eino run is required")
	}
	if emit == nil {
		return model.AgentRunFailed, fmt.Errorf("stream emitter is required")
	}

	// 推送 running 事件
	if err := emitExecutionEvent(emit, StreamEvent{
		Type: "run",
		Payload: RunSSEEvent{
			RunID:  prepared.Run.ID,
			Status: "running",
		},
	}); err != nil {
		// 落库失败
		return s.finalizeAfterStream(requestCtx, prepared, nil, err, emit, nil, nil, nil)
	}

	// 设置运行超时时间
	deadline := time.Now().Add(s.runs.runtime.RunTimeout())
	// 创建运行上下文
	runCtx, cancel := context.WithDeadline(requestCtx, deadline)
	defer cancel()

	// 运行 Eino Runner
	iterator := prepared.Runner.Run(runCtx, prepared.Messages)
	outputs := make([]*schema.AgenticMessage, 0, 1)
	var runErr error
	var interruption bool
	var promptTokens, completionTokens, totalTokens *uint32

	for {
		// 获取下一个 Eino 事件
		event, ok := iterator.Next()
		if !ok {
			break
		}
		// 事件为空
		if event == nil {
			logger.Error("event is nil")
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
			// WillRetryError 表示框架将自动重试，不算终态失败。
			var willRetry *adk.WillRetryError
			if !errors.As(event.Err, &willRetry) {
				runErr = event.Err
			}
		}
		// 处理消息输出
		if event.Output != nil && event.Output.MessageOutput != nil {
			messages, tokens, err := consumeMessageOutput(runCtx, event, emit)
			outputs = append(outputs, messages...)
			mergeTokenUsage(&promptTokens, &completionTokens, &totalTokens, tokens)
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
		// CheckPointStore TODO: interruption resume not implemented.
		runErr = fmt.Errorf("agent interruption is not supported without a CheckPointStore")
	}
	// 忽略 promptTokens
	_ = promptTokens
	// 确定终态并落库
	return s.finalizeAfterStream(runCtx, prepared, outputs, runErr, emit, completionTokens, totalTokens, promptTokens)
}

// finalizeAfterStream 根据 runCtx / runErr 判定终态，落库后尽力推送终态 SSE（emit 失败不再阻断落库）。
func (s *ExecutionService) finalizeAfterStream(
	runCtx context.Context,
	prepared *PreparedRun,
	outputs []*schema.AgenticMessage,
	runErr error,
	emit EmitFunc,
	completionTokens, totalTokens, promptTokens *uint32,
) (uint8, error) {
	// 确定终态
	status := model.AgentRunSucceeded
	errorCode := ""
	errorMessage := ""

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded): // 运行超时
		status = model.AgentRunFailed
		errorCode = "execution_timeout"
		errorMessage = "Run exceeded its execution deadline"
	case errors.Is(runCtx.Err(), context.Canceled): // 运行取消
		status = model.AgentRunCancelled
		errorCode = "cancelled"
		errorMessage = "Run was cancelled by the client"
	case errors.Is(runErr, errClientDisconnected): // 客户端断开
		status = model.AgentRunCancelled
		errorCode = "cancelled"
		errorMessage = "Run was cancelled because the client stream disconnected"
	case runErr != nil: // 运行错误
		status = model.AgentRunFailed
		errorCode = "agent_execution"
		errorMessage = runErr.Error()
	}

	// 落库使用独立短超时 context，避免客户端断开导致 requestCtx 取消而跳过持久化。
	finalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finalizeErr := s.runs.Finalize(finalCtx, prepared, FinalizeInput{
		Outputs:          outputs,
		Status:           status,
		ErrorCode:        errorCode,
		ErrorMessage:     errorMessage,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	})

	// 推送终态事件
	terminalStatus := statusName(status)
	_ = emitExecutionEvent(emit, StreamEvent{
		Type: "run",
		Payload: RunSSEEvent{
			RunID:  prepared.Run.ID,
			Status: terminalStatus,
			Error:  errorMessage,
		},
	})
	if finalizeErr != nil { // 落库失败
		return status, finalizeErr
	}
	return status, runErr
}

type tokenUsage struct {
	Prompt     *uint32
	Completion *uint32
	Total      *uint32
}

// consumeMessageOutput 处理单条 Agent 输出：整包 message 或流式 MessageStream，逐块 emit 后合并。
func consumeMessageOutput(
	ctx context.Context,
	event *adk.TypedAgentEvent[*schema.AgenticMessage],
	emit EmitFunc,
) ([]*schema.AgenticMessage, tokenUsage, error) {
	mo := event.Output.MessageOutput
	if mo.Message != nil {
		content := model.ExtractAgenticText(mo.Message)
		if content != "" {
			if err := emitExecutionEvent(emit, StreamEvent{
				Type: "message",
				Payload: EinoSSEEvent{
					Type:      "message",
					AgentName: event.AgentName,
					Content:   content,
				},
			}); err != nil {
				return nil, tokenUsage{}, err
			}
		}
		return []*schema.AgenticMessage{mo.Message}, tokensFromMessage(mo.Message), nil
	}
	if mo.MessageStream == nil {
		return nil, tokenUsage{}, nil
	}

	var chunks []*schema.AgenticMessage
	for {
		if err := ctx.Err(); err != nil {
			return nil, tokenUsage{}, err
		}
		chunk, err := mo.MessageStream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, tokenUsage{}, err
		}
		if chunk == nil {
			continue
		}
		chunks = append(chunks, chunk)
		text := model.ExtractAgenticText(chunk)
		if text == "" {
			continue
		}
		if err := emitExecutionEvent(emit, StreamEvent{
			Type: "stream_chunk",
			Payload: EinoSSEEvent{
				Type:      "stream_chunk",
				AgentName: event.AgentName,
				Content:   text,
			},
		}); err != nil {
			return nil, tokenUsage{}, err
		}
	}
	if len(chunks) == 0 {
		return nil, tokenUsage{}, nil
	}
	merged, err := schema.ConcatAgenticMessages(chunks)
	if err != nil {
		return nil, tokenUsage{}, err
	}
	return []*schema.AgenticMessage{merged}, tokensFromMessage(merged), nil
}

func emitAction(event *adk.TypedAgentEvent[*schema.AgenticMessage], emit EmitFunc) error {
	actionType := "action"
	if event.Action.Interrupted != nil {
		actionType = "interrupted"
	}
	return emitExecutionEvent(emit, StreamEvent{
		Type: "action",
		Payload: EinoSSEEvent{
			Type:       "action",
			AgentName:  event.AgentName,
			ActionType: actionType,
		},
	})
}

func einoErrorEvent(event *adk.TypedAgentEvent[*schema.AgenticMessage]) EinoSSEEvent {
	msg := ""
	if event.Err != nil {
		msg = event.Err.Error()
	}
	return EinoSSEEvent{
		Type:      "error",
		AgentName: event.AgentName,
		Error:     msg,
	}
}

// emitExecutionEvent 将 emit 失败统一映射为 errClientDisconnected，便于 finalize 判定为 cancelled。
func emitExecutionEvent(emit EmitFunc, event StreamEvent) error {
	if err := emit(event); err != nil {
		return errClientDisconnected
	}
	return nil
}

func tokensFromMessage(msg *schema.AgenticMessage) tokenUsage {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.TokenUsage == nil {
		return tokenUsage{}
	}
	u := msg.ResponseMeta.TokenUsage
	pt := uint32(u.PromptTokens)
	ct := uint32(u.CompletionTokens)
	tt := uint32(u.TotalTokens)
	return tokenUsage{Prompt: &pt, Completion: &ct, Total: &tt}
}

// mergeTokenUsage 取最近一次非 nil 的 token 统计（Eino 通常在最终 message 上携带 usage）。
func mergeTokenUsage(prompt, completion, total **uint32, next tokenUsage) {
	if next.Prompt != nil {
		*prompt = next.Prompt
	}
	if next.Completion != nil {
		*completion = next.Completion
	}
	if next.Total != nil {
		*total = next.Total
	}
}

func statusName(status uint8) string {
	switch status {
	case model.AgentRunSucceeded:
		return "succeeded"
	case model.AgentRunCancelled:
		return "cancelled"
	case model.AgentRunFailed:
		return "failed"
	default:
		return "running"
	}
}
