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

type EinoSSEEvent struct {
	Type       string `json:"type"`
	AgentName  string `json:"agent_name,omitempty"`
	RunPath    string `json:"run_path,omitempty"`
	Content    string `json:"content,omitempty"`
	ActionType string `json:"action_type,omitempty"`
	Error      string `json:"error,omitempty"`
}

type RunSSEEvent struct {
	RunID  uint64 `json:"run_id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type ExecutionService struct {
	runs *RunService
}

func NewExecutionService(runs *RunService) *ExecutionService {
	return &ExecutionService{runs: runs}
}

func (s *ExecutionService) Run(
	requestCtx context.Context,
	prepared *PreparedRun,
	emit EmitFunc,
) (uint8, error) {
	if prepared == nil || prepared.Run == nil || prepared.Runner == nil {
		return model.AgentRunFailed, fmt.Errorf("prepared Eino run is required")
	}
	if emit == nil {
		return model.AgentRunFailed, fmt.Errorf("stream emitter is required")
	}

	if err := emitExecutionEvent(emit, StreamEvent{
		Type: "run",
		Payload: RunSSEEvent{
			RunID:  prepared.Run.ID,
			Status: "running",
		},
	}); err != nil {
		return s.finalizeAfterStream(requestCtx, prepared, nil, err, emit, nil, nil, nil)
	}

	deadline := time.Now().Add(s.runs.runtime.RunTimeout())
	runCtx, cancel := context.WithDeadline(requestCtx, deadline)
	defer cancel()

	iterator := prepared.Runner.Run(runCtx, prepared.Messages)
	outputs := make([]*schema.AgenticMessage, 0, 1)
	var runErr error
	var interruption bool
	var promptTokens, completionTokens, totalTokens *uint32

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
	_ = promptTokens
	return s.finalizeAfterStream(runCtx, prepared, outputs, runErr, emit, completionTokens, totalTokens, promptTokens)
}

func (s *ExecutionService) finalizeAfterStream(
	runCtx context.Context,
	prepared *PreparedRun,
	outputs []*schema.AgenticMessage,
	runErr error,
	emit EmitFunc,
	completionTokens, totalTokens, promptTokens *uint32,
) (uint8, error) {
	status := model.AgentRunSucceeded
	errorCode := ""
	errorMessage := ""

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		status = model.AgentRunFailed
		errorCode = "execution_timeout"
		errorMessage = "Run exceeded its execution deadline"
	case errors.Is(runCtx.Err(), context.Canceled):
		status = model.AgentRunCancelled
		errorCode = "cancelled"
		errorMessage = "Run was cancelled by the client"
	case errors.Is(runErr, errClientDisconnected):
		status = model.AgentRunCancelled
		errorCode = "cancelled"
		errorMessage = "Run was cancelled because the client stream disconnected"
	case runErr != nil:
		status = model.AgentRunFailed
		errorCode = "agent_execution"
		errorMessage = runErr.Error()
	}

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

	terminalStatus := statusName(status)
	_ = emitExecutionEvent(emit, StreamEvent{
		Type: "run",
		Payload: RunSSEEvent{
			RunID:  prepared.Run.ID,
			Status: terminalStatus,
			Error:  errorMessage,
		},
	})
	if finalizeErr != nil {
		return status, finalizeErr
	}
	return status, runErr
}

type tokenUsage struct {
	Prompt     *uint32
	Completion *uint32
	Total      *uint32
}

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
